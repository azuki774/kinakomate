package restore

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
	"github.com/azuki774/kinakomate/internal/log"
)

// scaleTimeout bounds how long the runner waits for a workload to reach the
// desired replica count before failing and rolling back.
const scaleTimeout = 5 * time.Minute

// scalePollInterval is the polling period used while waiting for replicas.
const scalePollInterval = 2 * time.Second

// Database abstracts the misskey database operations the runner needs.
type Database interface {
	// CheckConnection verifies the runner can reach the PostgreSQL server.
	CheckConnection(ctx context.Context, cfg *config.Config) error
	// Reset recreates the target database from template0 so the restore
	// always starts from an empty database.
	Reset(ctx context.Context, cfg *config.Config) error
	// Restore restores the downloaded dump into the database.
	Restore(ctx context.Context, cfg *config.Config, dump *Dump) error
}

// ObjectStorage abstracts the S3 operations the runner needs.
type ObjectStorage interface {
	// CheckConnection verifies the runner can reach the object storage.
	CheckConnection(ctx context.Context, cfg *config.Config) error
	// DownloadAndExtract fetches and validates the gzip dump, staging it on
	// disk, and returns a handle the caller must later clean up.
	DownloadAndExtract(ctx context.Context, cfg *config.Config) (*Dump, error)
}

// Kubernetes abstracts the workload scaling the runner needs.
//
// The scale / readiness control (issue #4) is implemented here against the
// real Kubernetes API, scaling the web and db workloads by name.
type Kubernetes interface {
	// CheckConnection verifies the runner can reach the Kubernetes API.
	CheckConnection(ctx context.Context, cfg *config.Config) error
	// GetReplicas returns the desired replica count of the named workload.
	GetReplicas(ctx context.Context, cfg *config.Config, workload string) (int, error)
	// Scale sets the replica count of the named workload.
	Scale(ctx context.Context, cfg *config.Config, workload string, replicas int) error
	// WaitForReplicas polls until the named workload reaches the desired
	// replica count, failing on timeout.
	WaitForReplicas(ctx context.Context, cfg *config.Config, workload string, want int, timeout time.Duration) error
}

// runner wires the per-target dependencies together and runs the restore-test
// workflow in a fixed order.
type runner struct {
	db     Database
	s3     ObjectStorage
	k8s    Kubernetes
	api    MisskeyAPI
	logger *slog.Logger
}

// newRunner builds a runner wired to the real dependencies: the Kubernetes
// client, object storage, database, and Misskey HTTP API client.
func newRunner(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*runner, error) {
	if logger == nil {
		logger = log.New()
	}
	k8s, err := newKubernetesClient()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize kubernetes client: %w", err)
	}
	s3, err := newObjectStorage(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}
	return &runner{
		db:     newDatabase(logger),
		s3:     s3,
		k8s:    k8s,
		api:    newMisskeyAPI(logger),
		logger: logger,
	}, nil
}

// run preserves the original two-argument test API by supplying an already
// initialized local result. Run uses runWithResult after preflight instead.
func (r *runner) run(ctx context.Context, cfg *config.Config) error {
	result := newExecutionResult(time.Now())
	if err := result.beginPhase(phasePreflight, time.Now()); err != nil {
		return err
	}
	if err := result.completePhase(time.Now()); err != nil {
		return err
	}
	result.markInitialized()
	return r.runWithResult(ctx, cfg, result)
}

type runnerStep struct {
	name string
	fn   func(context.Context, *config.Config) error
}

func (r *runner) runWithResult(ctx context.Context, cfg *config.Config, result *executionResult) (err error) {
	var dump *Dump
	defer func() {
		if dump == nil {
			return
		}
		cleanupErr := dump.cleanup()
		if cleanupErr == nil {
			return
		}
		cleanupFailure := fmt.Errorf("staged dump cleanup failed: %w", cleanupErr)
		attrs := []any{"phase", phaseCleanup, "err", cleanupErr}
		if err != nil || result.failedPhase != "" {
			attrs = append(attrs, "recovery_status", "error")
		}
		r.log().ErrorContext(ctx, "staged dump cleanup failed", attrs...)
		if err == nil && result.failedPhase == "" {
			result.failCompletedPhase(phaseCleanup, cleanupFailure)
			err = cleanupFailure
			return
		}
		result.recordRecoveryError(cleanupFailure)
	}()

	prepare := []runnerStep{
		{"record replicas", func(ctx context.Context, cfg *config.Config) error {
			r.recordReplicas(ctx, cfg)
			return nil
		}},
		{"db connection check", r.db.CheckConnection},
		{"s3 connection check", r.s3.CheckConnection},
		{"kubernetes api connection check", r.k8s.CheckConnection},
		{"s3 download + decompress", func(ctx context.Context, cfg *config.Config) error {
			var err error
			dump, err = r.s3.DownloadAndExtract(ctx, cfg)
			if err == nil {
				result.setObject(dump)
			}
			return err
		}},
	}
	restore := []runnerStep{
		{"scale web to 0", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.Scale(ctx, cfg, cfg.WebWorkload, 0)
		}},
		{"scale db to 1", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.Scale(ctx, cfg, cfg.DBWorkload, 1)
		}},
		{"wait web replicas 0", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.WaitForReplicas(ctx, cfg, cfg.WebWorkload, 0, scaleTimeout)
		}},
		{"wait db replicas 1", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.WaitForReplicas(ctx, cfg, cfg.DBWorkload, 1, scaleTimeout)
		}},
		{"reset database", r.db.Reset},
		{"db restore", func(ctx context.Context, cfg *config.Config) error {
			return r.db.Restore(ctx, cfg, dump)
		}},
	}
	verify := []runnerStep{
		{"scale web to 1", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.Scale(ctx, cfg, cfg.WebWorkload, 1)
		}},
		{"wait web replicas 1", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.WaitForReplicas(ctx, cfg, cfg.WebWorkload, 1, scaleTimeout)
		}},
		{"wait for Misskey API readiness", func(ctx context.Context, cfg *config.Config) error {
			return r.api.WaitForReadiness(ctx, cfg, scaleTimeout)
		}},
		{"リストアデータのGTL取得確認", r.api.CheckGlobalTimeline},
	}
	cleanup := []runnerStep{
		{"scale web to 0", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.Scale(ctx, cfg, cfg.WebWorkload, 0)
		}},
		{"scale db to 0", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.Scale(ctx, cfg, cfg.DBWorkload, 0)
		}},
	}

	for _, group := range []struct {
		phase phaseName
		steps []runnerStep
	}{
		{phasePrepare, prepare},
		{phaseRestore, restore},
		{phaseVerify, verify},
		{phaseCleanup, cleanup},
	} {
		if err := r.runPhase(ctx, cfg, result, group.phase, group.steps); err != nil {
			r.recover(ctx, cfg, result)
			return err
		}
	}
	return nil
}

func (r *runner) runPhase(ctx context.Context, cfg *config.Config, result *executionResult, phase phaseName, steps []runnerStep) error {
	logger := r.log()
	if err := result.beginPhase(phase, time.Now()); err != nil {
		return err
	}
	for _, step := range steps {
		started := time.Now()
		logger.InfoContext(ctx, "restore-test step start", "phase", phase, "step", step.name)
		if err := step.fn(ctx, cfg); err != nil {
			durationMS, duration := durationFields(time.Since(started))
			wrapped := fmt.Errorf("%s phase step %q failed: %w", phase, step.name, err)
			logger.ErrorContext(ctx, "restore-test step failed", "phase", phase, "step", step.name, "duration_ms", durationMS, "duration", duration, "err", err)
			if stateErr := result.failPhase(phase, time.Now(), wrapped); stateErr != nil {
				return fmt.Errorf("%w: record phase failure: %v", wrapped, stateErr)
			}
			return wrapped
		}
		durationMS, duration := durationFields(time.Since(started))
		logger.InfoContext(ctx, "restore-test step done", "phase", phase, "step", step.name, "duration_ms", durationMS, "duration", duration)
	}
	return result.completePhase(time.Now())
}

func (r *runner) recover(ctx context.Context, cfg *config.Config, result *executionResult) {
	if !result.recoveryRequired() {
		return
	}
	logger := r.log()
	rbCtx := context.WithoutCancel(ctx)
	if err := result.beginPhase(phaseCleanup, time.Now()); err != nil {
		logger.ErrorContext(rbCtx, "rollback: failed to scale web to 0", "phase", phaseCleanup, "step", "scale web to 0", "recovery_status", "error", "err", err)
		return
	}

	started := time.Now()
	logger.InfoContext(rbCtx, "restore-test recovery step start", "phase", phaseCleanup, "step", "scale web to 0")
	err := r.k8s.Scale(rbCtx, cfg, cfg.WebWorkload, 0)
	durationMS, duration := durationFields(time.Since(started))
	if err != nil {
		recoveryErr := fmt.Errorf("cleanup recovery step %q failed: %w", "scale web to 0", err)
		logger.ErrorContext(rbCtx, "rollback: failed to scale web to 0", "phase", phaseCleanup, "step", "scale web to 0", "duration_ms", durationMS, "duration", duration, "recovery_status", "error", "err", err)
		if stateErr := result.failPhase(phaseCleanup, time.Now(), recoveryErr); stateErr != nil {
			logger.ErrorContext(rbCtx, "restore-test recovery report failed", "phase", phaseCleanup, "err", stateErr)
		}
		return
	}
	logger.InfoContext(rbCtx, "rollback: scaled web to 0", "phase", phaseCleanup, "step", "scale web to 0", "duration_ms", durationMS, "duration", duration, "recovery_status", "success")
	if err := result.completePhase(time.Now()); err != nil {
		logger.ErrorContext(rbCtx, "restore-test recovery report failed", "phase", phaseCleanup, "err", err)
	}
}

// recordReplicas logs the current replica count of the web and db workloads.
// It is best-effort: a read failure is logged but does not stop the run.
func (r *runner) recordReplicas(ctx context.Context, cfg *config.Config) {
	logger := r.log()
	for _, w := range []string{cfg.WebWorkload, cfg.DBWorkload} {
		n, err := r.k8s.GetReplicas(ctx, cfg, w)
		if err != nil {
			logger.WarnContext(ctx, "failed to read current replicas", "workload", w, "err", err)
			continue
		}
		logger.InfoContext(ctx, "current replicas", "workload", w, "replicas", n)
	}
}

func (r *runner) log() *slog.Logger {
	if r.logger == nil {
		return log.New()
	}
	return r.logger
}
