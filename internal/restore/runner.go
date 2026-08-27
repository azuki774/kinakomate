package restore

import (
	"context"
	"fmt"
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
//
// The restore step (issue #5) and the connection check fill these in later.
type Database interface {
	// CheckConnection verifies the runner can reach the target database.
	CheckConnection(ctx context.Context, cfg *config.Config) error
	// Restore restores the downloaded dump into the database.
	Restore(ctx context.Context, cfg *config.Config) error
}

// ObjectStorage abstracts the S3 operations the runner needs.
//
// The download + decompress step (issue #5) fills this in later.
type ObjectStorage interface {
	// CheckConnection verifies the runner can reach the object storage.
	CheckConnection(ctx context.Context, cfg *config.Config) error
	// DownloadAndExtract fetches and decompresses the dump.
	DownloadAndExtract(ctx context.Context, cfg *config.Config) error
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

// Checks abstracts the post-restore verification commands (issue #7).
type Checks interface {
	// Run executes the ordered verification commands.
	Run(ctx context.Context, cfg *config.Config) error
}

// runner wires the per-target dependencies together and runs the restore-test
// workflow in a fixed order.
type runner struct {
	db  Database
	s3  ObjectStorage
	k8s Kubernetes
	chk Checks
}

// buildRunner constructs the production runner. It is a package-level variable
// so tests can substitute a mock runner without reaching a real cluster.
var buildRunner = func() (*runner, error) {
	return newRunner()
}

// newRunner builds a runner with the real Kubernetes client and placeholder
// no-op dependencies for the steps implemented in later issues.
func newRunner() (*runner, error) {
	k8s, err := newKubernetesClient()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize kubernetes client: %w", err)
	}
	return &runner{
		db:  databaseTODO{},
		s3:  objectStorageTODO{},
		k8s: k8s,
		chk: checksTODO{},
	}, nil
}

// run executes the restore-test workflow in order:
//
//  1. DB connection check
//  2. S3 connection check
//  3. Kubernetes API connection check
//  4. record current replica counts (for audit / recovery)
//  5. S3 download + decompress
//  6. T1: scale web to 0, db to 1
//  7. wait for web replicas to reach 0
//  8. DB restore
//  9. T2: scale web to 1
// 10. checks
// 11. T3 (cleanup): scale web to 0, db to 0
//
// On the first error it stops immediately and returns the wrapped error; the
// remaining steps are not executed. A deferred rollback scales web back to 0
// on failure (the azkey/web workload is the one that must never stay up after a
// failed run).
func (r *runner) run(ctx context.Context, cfg *config.Config) error {
	logger := log.New()

	// Record the pre-run replica counts so a later run (or operator) can
	// restore the original state if needed.
	r.recordReplicas(ctx, cfg)

	failed := true
	defer func() {
		if !failed {
			return
		}
		// Rollback: ensure web is scaled back to 0 on any failure.
		rbCtx := context.WithoutCancel(ctx)
		if err := r.k8s.Scale(rbCtx, cfg, cfg.WebWorkload, 0); err != nil {
			logger.ErrorContext(ctx, "rollback: failed to scale web to 0", "err", err)
		} else {
			logger.InfoContext(ctx, "rollback: scaled web to 0", "workload", cfg.WebWorkload)
		}
	}()

	type step struct {
		name string
		fn   func(context.Context, *config.Config) error
	}

	steps := []step{
		{"db connection check", r.db.CheckConnection},
		{"s3 connection check", r.s3.CheckConnection},
		{"kubernetes api connection check", r.k8s.CheckConnection},
		{"s3 download + decompress", r.s3.DownloadAndExtract},
		{"scale web to 0", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.Scale(ctx, cfg, cfg.WebWorkload, 0)
		}},
		{"scale db to 1", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.Scale(ctx, cfg, cfg.DBWorkload, 1)
		}},
		{"wait web replicas 0", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.WaitForReplicas(ctx, cfg, cfg.WebWorkload, 0, scaleTimeout)
		}},
		{"db restore", r.db.Restore},
		{"scale web to 1", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.Scale(ctx, cfg, cfg.WebWorkload, 1)
		}},
		{"checks", r.chk.Run},
		{"cleanup: scale web to 0", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.Scale(ctx, cfg, cfg.WebWorkload, 0)
		}},
		{"cleanup: scale db to 0", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.Scale(ctx, cfg, cfg.DBWorkload, 0)
		}},
	}

	for _, s := range steps {
		logger.InfoContext(ctx, "restore-test step start", "step", s.name)
		if err := s.fn(ctx, cfg); err != nil {
			return fmt.Errorf("restore-test step %q failed: %w", s.name, err)
		}
		logger.InfoContext(ctx, "restore-test step done", "step", s.name)
	}

	failed = false
	return nil
}

// recordReplicas logs the current replica count of the web and db workloads.
// It is best-effort: a read failure is logged but does not stop the run.
func (r *runner) recordReplicas(ctx context.Context, cfg *config.Config) {
	logger := log.New()
	for _, w := range []string{cfg.WebWorkload, cfg.DBWorkload} {
		n, err := r.k8s.GetReplicas(ctx, cfg, w)
		if err != nil {
			logger.WarnContext(ctx, "failed to read current replicas", "workload", w, "err", err)
			continue
		}
		logger.InfoContext(ctx, "current replicas", "workload", w, "replicas", n)
	}
}

// databaseTODO is a placeholder Database dependency. Real logic lands in issue #5.
type databaseTODO struct{}

func (databaseTODO) CheckConnection(_ context.Context, _ *config.Config) error {
	// TODO: implement misskey DB connection check.
	return nil
}

func (databaseTODO) Restore(_ context.Context, _ *config.Config) error {
	// TODO: implement DB restore.
	return nil
}

// objectStorageTODO is a placeholder ObjectStorage dependency. Real logic lands in issue #5.
type objectStorageTODO struct{}

func (objectStorageTODO) CheckConnection(_ context.Context, _ *config.Config) error {
	// TODO: implement S3 connection check.
	return nil
}

func (objectStorageTODO) DownloadAndExtract(_ context.Context, _ *config.Config) error {
	// TODO: implement S3 download + decompress.
	return nil
}

// checksTODO is a placeholder Checks dependency. Real logic lands in issue #7.
type checksTODO struct{}

func (checksTODO) Run(_ context.Context, _ *config.Config) error {
	// TODO: implement verification checks.
	return nil
}
