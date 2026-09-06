package restore

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
	"github.com/azuki774/kinakomate/internal/log"
)

// Run executes the `restore-test` subcommand.
//
// It validates the input contract before constructing the runner, then executes
// the restore-test workflow and emits one final structured report. The runner
// fetches the dump, restores it into a fresh database, starts the web workload,
// waits for the Misskey API, validates the global timeline, and cleans up the
// workloads.
func Run(ctx context.Context, args []string) error {
	return runWithLogger(ctx, args, log.New())
}

func runWithLogger(ctx context.Context, args []string, logger *slog.Logger) (err error) {
	if logger == nil {
		logger = log.New()
	}
	result := newExecutionResult(time.Now())
	defer func() {
		if err != nil && result.firstErr == nil && result.currentPhase != "" {
			_ = result.failPhase(result.currentPhase, time.Now(), err)
		}
		report := result.report(time.Now())
		attrs := []any{
			"result", report.Result,
			"total_duration_ms", report.TotalDurationMS,
			"total_duration", report.TotalDuration,
			"phases", report.Phases,
		}
		if report.FailedPhase != "" {
			attrs = append(attrs, "failed_phase", report.FailedPhase)
		}
		if report.Error != "" {
			attrs = append(attrs, "error", report.Error)
		}
		if report.RecoveryStatus != "" {
			attrs = append(attrs, "recovery_status", report.RecoveryStatus)
		}
		if report.RecoveryError != "" {
			attrs = append(attrs, "recovery_error", report.RecoveryError)
		}
		if report.Object != nil {
			attrs = append(attrs, "object", report.Object)
		}
		logger.InfoContext(ctx, "restore-test final report", attrs...)
	}()

	if err = result.beginPhase(phasePreflight, time.Now()); err != nil {
		return err
	}
	fs := flag.NewFlagSet("restore-test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err = fs.Parse(args); err != nil {
		err = fmt.Errorf("parse restore-test flags: %w", err)
		_ = result.failPhase(phasePreflight, time.Now(), err)
		return err
	}

	var cfg *config.Config
	cfg, err = config.LoadFromEnv()
	if err != nil {
		err = fmt.Errorf("pre-flight validation failed: %w", err)
		_ = result.failPhase(phasePreflight, time.Now(), err)
		return err
	}

	logger.InfoContext(ctx, "pre-flight validation passed", configToArgs(cfg.Loggable())...)

	var r *runner
	r, err = runnerFactory(ctx, cfg, logger)
	if err != nil {
		err = fmt.Errorf("initialize runner: %w", err)
		_ = result.failPhase(phasePreflight, time.Now(), err)
		return err
	}
	result.markInitialized()
	if err = result.completePhase(time.Now()); err != nil {
		return err
	}
	return r.runWithResult(ctx, cfg, result)
}

// runnerFactory builds a runner with the real dependencies. It is a variable so
// tests can substitute a runner backed by fakes and keep the run hermetic.
var runnerFactory = func(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*runner, error) {
	return newRunner(ctx, cfg, logger)
}

// configToArgs flattens a map into alternating key/value arguments for
// slog's key-value logging style.
func configToArgs(m map[string]any) []any {
	args := make([]any, 0, len(m)*2)
	for k, v := range m {
		args = append(args, k, v)
	}
	return args
}
