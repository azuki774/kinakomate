package backup

import (
	"context"
	"errors"
	"flag"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
	"github.com/azuki774/kinakomate/internal/log"
)

type backupDependencies struct {
	loadConfig func() (*config.BackupConfig, error)
	newStore   func(context.Context, *config.BackupConfig) (objectStore, error)
	createTemp func() (dumpFile, error)
	openDump   func(string) (openedDump, error)
	dump       func(context.Context, *config.BackupConfig, io.Writer) error
	validate   func(context.Context, string) (int64, bool, error)
	remove     func(string) error
}

func defaultDependencies() backupDependencies {
	return backupDependencies{
		loadConfig: config.LoadBackupFromEnv,
		newStore:   newS3ObjectStore,
		createTemp: func() (dumpFile, error) {
			return os.CreateTemp("", "kinakomate-backup-*.sql.gz")
		},
		openDump: func(path string) (openedDump, error) {
			return os.Open(path)
		},
		dump:     dump,
		validate: validateGzip,
		remove:   os.Remove,
	}
}

// RunWithSummary runs one backup and returns its safe operational summary after
// attempting temporary-file cleanup.
func RunWithSummary(ctx context.Context, args []string) (RunSummary, error) {
	return runWithSummary(ctx, args, nil)
}

func runWithSummary(ctx context.Context, args []string, logger *slog.Logger) (RunSummary, error) {
	return runWithDependencies(ctx, args, defaultDependencies(), logger)
}

func runWithDependencies(ctx context.Context, args []string, deps backupDependencies, logger *slog.Logger) (RunSummary, error) {
	result := newRunResult(time.Now())
	parseErr := parseBackupArguments(args)
	if errors.Is(parseErr, flag.ErrHelp) {
		summary, _ := result.finish()
		return summary, flag.ErrHelp
	}
	if logger == nil {
		logger = log.New()
	}
	if parseErr != nil {
		result.runPhase(0, func() error { return parseErr })
		return finishRun(ctx, result, logger)
	}

	var cfg *config.BackupConfig
	var store objectStore
	var configErr error
	if !result.runPhase(0, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		cfg, configErr = deps.loadConfig()
		if configErr != nil || cfg == nil {
			return errors.New("backup configuration unavailable")
		}
		// Retain the exact target identity as soon as configuration is known so
		// that storage or later failures still report the real database and
		// object instead of re-reading the environment at notification time.
		result.summary.DatabaseName = cfg.DBName
		result.summary.S3Bucket = cfg.S3Bucket
		result.summary.S3Key = cfg.S3Key
		if err := ctx.Err(); err != nil {
			return err
		}
		store, err = deps.newStore(ctx, cfg)
		if err != nil || store == nil {
			return errors.New("backup storage unavailable")
		}
		return nil
	}) {
		// Configuration errors contain only the failing environment variable
		// name; SDK initialization details are intentionally replaced.
		if configErr != nil {
			result.err = configErr
		}
		return finishRun(ctx, result, logger)
	}

	var tempPath string
	var temp dumpFile
	dumpSucceeded := result.runPhase(1, func() error {
		if err := ctx.Err(); err != nil {
			return errors.New("backup canceled")
		}
		var err error
		temp, err = deps.createTemp()
		if err != nil || temp == nil {
			return errors.New("temporary dump file unavailable")
		}
		tempPath = temp.Name()
		if err := temp.Chmod(0o600); err != nil {
			if closeErr := temp.Close(); closeErr != nil {
				return errors.New("temporary dump file close failed")
			}
			return errors.New("temporary dump file setup failed")
		}
		dumpErr := deps.dump(ctx, cfg, temp)
		closeErr := temp.Close()
		if dumpErr != nil || closeErr != nil {
			return errors.New("dump generation failed")
		}
		return nil
	})

	if dumpSucceeded {
		validateSucceeded := result.runPhase(2, func() error {
			size, available, err := deps.validate(ctx, tempPath)
			if available {
				result.summary.BackupSize = size
				result.summary.BackupSizeAvailable = true
			}
			if err != nil {
				return errors.New("gzip validation failed")
			}
			return nil
		})
		if validateSucceeded {
			result.runPhase(3, func() error {
				if err := ctx.Err(); err != nil {
					return errors.New("backup canceled")
				}
				file, err := deps.openDump(tempPath)
				if err != nil || file == nil {
					return errors.New("open dump for upload failed")
				}
				uploadErr := store.Upload(ctx, cfg.S3Bucket, cfg.S3Key, file)
				closeErr := file.Close()
				if uploadErr != nil {
					return errors.New("S3 upload failed")
				}
				remoteSize, headErr := store.HeadObjectSize(ctx, cfg.S3Bucket, cfg.S3Key)
				if headErr != nil || remoteSize != result.summary.BackupSize {
					return errors.New("S3 object size verification failed")
				}
				result.summary.UploadVerified = true
				if closeErr != nil {
					return errors.New("close uploaded dump failed")
				}
				return nil
			})
		}
	}

	if tempPath != "" {
		result.runPhase(4, func() error {
			err := deps.remove(tempPath)
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		})
	}
	return finishRun(ctx, result, logger)
}

func parseBackupArguments(args []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	return nil
}

func finishRun(ctx context.Context, result *runResult, logger *slog.Logger) (RunSummary, error) {
	summary, err := result.finish()
	logFinalReport(ctx, logger, summary, err)
	return summary, err
}

type reportPhase struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Duration   string `json:"duration"`
}

func logFinalReport(ctx context.Context, logger *slog.Logger, summary RunSummary, err error) {
	status := "success"
	if err != nil {
		status = "failure"
	}
	phases := make([]reportPhase, len(summary.Phases))
	for i, phase := range summary.Phases {
		phases[i] = reportPhase{
			Name:       phase.Name,
			Status:     phase.Status,
			DurationMS: phase.Duration.Milliseconds(),
			Duration:   phase.Duration.String(),
		}
	}
	attrs := make([]any, 0, 24)
	attrs = append(attrs,
		"result", status,
		"total_duration_ms", summary.Total.Milliseconds(),
		"total_duration", summary.Total.String(),
		"phases", phases,
		"backup_size_available", summary.BackupSizeAvailable,
		"upload_verified", summary.UploadVerified,
	)
	if summary.FailedPhase != "" {
		attrs = append(attrs, "failed_phase", summary.FailedPhase)
	}
	if summary.BackupSizeAvailable {
		attrs = append(attrs, "backup_size_bytes", summary.BackupSize)
	}
	attrs = append(attrs,
		"temp_cleanup_status", summary.Phases[4].Status,
		"temp_cleanup_duration_ms", summary.Phases[4].Duration.Milliseconds(),
		"temp_cleanup_duration", summary.Phases[4].Duration.String(),
	)
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}
	logger.InfoContext(ctx, "backup final report", attrs...)
}
