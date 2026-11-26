package federation

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
	"github.com/azuki774/kinakomate/internal/log"
)

type federationDependencies struct {
	loadConfig     func() (*config.FederationReportConfig, error)
	newClient      func() Client
	aggregate      func(time.Time, *config.FederationReportConfig, MetaResponse, []InstanceRecord) Report
	renderMarkdown func(Report) string
	writeReport    func(string, string) error
	now            func() time.Time
}

func defaultFederationDependencies() federationDependencies {
	return federationDependencies{
		loadConfig:     config.LoadFederationReportFromEnv,
		newClient:      NewHTTPClient,
		aggregate:      Aggregate,
		renderMarkdown: RenderMarkdown,
		writeReport:    writeReportFile,
		now:            time.Now,
	}
}

// RunWithSummary runs one federation report and returns its safe operational
// summary.
func RunWithSummary(ctx context.Context, args []string) (RunSummary, error) {
	return runWithSummary(ctx, args, nil)
}

func runWithSummary(ctx context.Context, args []string, logger *slog.Logger) (RunSummary, error) {
	return runWithDependencies(ctx, args, defaultFederationDependencies(), logger)
}

func runWithDependencies(ctx context.Context, args []string, deps federationDependencies, logger *slog.Logger) (RunSummary, error) {
	result := newRunResult(deps.now())
	parseErr := parseFederationArguments(args)
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

	var cfg *config.FederationReportConfig
	var configErr error
	if !result.runPhase(0, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		cfg, configErr = deps.loadConfig()
		if configErr != nil || cfg == nil {
			return errors.New("federation-report configuration unavailable")
		}
		result.summary.OutputPath = cfg.OutputPath
		if err := ensureOutputDirectory(cfg.OutputPath); err != nil {
			return err
		}
		return ctx.Err()
	}) {
		// Configuration errors contain only the failing environment variable
		// name, so they are safe to surface instead of the fixed phase error.
		if configErr != nil {
			result.err = configErr
		}
		return finishRun(ctx, result, logger)
	}

	var report Report
	if !result.runPhase(1, func() error {
		if err := ctx.Err(); err != nil {
			return errors.New("federation-report canceled")
		}
		client := deps.newClient()
		if client == nil {
			return errors.New("federation client unavailable")
		}
		meta, err := client.FetchMeta(ctx, cfg)
		if err != nil {
			return errors.New("federation metainfo fetch failed")
		}
		if err := ctx.Err(); err != nil {
			return errors.New("federation-report canceled")
		}
		instances, err := client.FetchInstances(ctx, cfg)
		if err != nil {
			return errors.New("federation instance fetch failed")
		}
		if err := ctx.Err(); err != nil {
			return errors.New("federation-report canceled")
		}
		report = deps.aggregate(deps.now(), cfg, meta, instances)
		result.summary.TotalInstances = report.Overview.TotalCount
		result.summary.ActiveInstances = report.Overview.ActiveCount
		result.summary.NotRespondingInstances = report.Overview.NotRespondingCount
		result.summary.SuspendedInstances = report.Overview.SuspendedCount
		result.summary.BlockedInstances = report.Overview.BlockedCount
		result.summary.NewInstancesCount = report.NewInstances.Total
		result.summary.UnhealthyInstancesCount = report.UnhealthyInstances.UnhealthyTotal
		result.summary.UnhealthyWithFFCount = report.UnhealthyInstances.UnhealthyWithFFCount
		return nil
	}) {
		return finishRun(ctx, result, logger)
	}

	result.runPhase(2, func() error {
		if err := ctx.Err(); err != nil {
			return errors.New("federation-report canceled")
		}
		markdown := deps.renderMarkdown(report)
		if err := deps.writeReport(cfg.OutputPath, markdown); err != nil {
			return errors.New("federation report write failed")
		}
		return nil
	})
	return finishRun(ctx, result, logger)
}

func parseFederationArguments(args []string) error {
	flags := flag.NewFlagSet("federation-report", flag.ContinueOnError)
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

func ensureOutputDirectory(path string) error {
	info, err := os.Stat(filepath.Dir(path))
	if err != nil || !info.IsDir() {
		return errors.New("federation-report output directory is unavailable")
	}
	return nil
}

// writeReportFile writes content to a temporary file next to path and renames
// it into place, so a failed run never leaves a partial report behind.
func writeReportFile(path, content string) error {
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, ".federation-report-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tempPath)
		}
	}()

	if err := temp.Chmod(0o644); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := io.WriteString(temp, content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	renamed = true
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

	attrs := []any{
		"result", status,
		"total_duration_ms", summary.Total.Milliseconds(),
		"total_duration", summary.Total.String(),
		"phases", phases,
	}
	if summary.FailedPhase != "" {
		attrs = append(attrs, "failed_phase", summary.FailedPhase)
	}
	attrs = append(attrs,
		"output_path", summary.OutputPath,
		"total_instances", summary.TotalInstances,
		"active_instances", summary.ActiveInstances,
		"not_responding_instances", summary.NotRespondingInstances,
		"suspended_instances", summary.SuspendedInstances,
		"blocked_instances", summary.BlockedInstances,
		"new_instances_count", summary.NewInstancesCount,
		"unhealthy_instances_count", summary.UnhealthyInstancesCount,
		"unhealthy_with_ff_count", summary.UnhealthyWithFFCount,
	)
	logger.InfoContext(ctx, "federation-report final report", attrs...)
}
