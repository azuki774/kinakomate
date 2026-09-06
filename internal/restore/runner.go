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
	db  Database
	s3  ObjectStorage
	k8s Kubernetes
	api MisskeyAPI
}

// newRunner builds a runner wired to the real dependencies: the Kubernetes
// client, object storage, database, and Misskey HTTP API client.
func newRunner(ctx context.Context, cfg *config.Config) (*runner, error) {
	k8s, err := newKubernetesClient()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize kubernetes client: %w", err)
	}
	s3, err := newObjectStorage(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &runner{
		db:  newDatabase(),
		s3:  s3,
		k8s: k8s,
		api: newMisskeyAPI(),
	}, nil
}

// run executes the restore-test workflow in order:
//
//  1. record current replica counts (for audit / recovery)
//  2. DB connection check
//  3. S3 connection check
//  4. Kubernetes API connection check
//  5. S3 download + decompress (stages the gzip dump on disk)
//  6. T1: scale web to 0, db to 1
//  7. wait for web replicas to reach 0 and db replicas to reach 1
//  8. reset database (terminate backends, DROP IF EXISTS, CREATE from
//     template0 — a plain SQL dump does not clean the target itself)
//  9. DB restore (streams the gzip into psql, single transaction)
//
// 10. T2: scale web to 1
// 11. wait for web replicas to reach 1
// 12. wait for Misskey API readiness
// 13. check the Misskey global timeline
// 14. T3 (cleanup): scale web to 0, db to 0
//
// On the first error it stops immediately and returns the wrapped error; the
// remaining steps are not executed and the staged dump is removed. A deferred
// rollback scales web back to 0 on failure (the web workload must never stay
// up after a failed run).
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

	var dump *Dump
	steps := []step{
		{"db connection check", r.db.CheckConnection},
		{"s3 connection check", r.s3.CheckConnection},
		{"kubernetes api connection check", r.k8s.CheckConnection},
		{"s3 download + decompress", func(ctx context.Context, cfg *config.Config) error {
			var err error
			dump, err = r.s3.DownloadAndExtract(ctx, cfg)
			return err
		}},
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
			if dump != nil {
				dump.Cleanup()
			}
			return fmt.Errorf("restore-test step %q failed: %w", s.name, err)
		}
		logger.InfoContext(ctx, "restore-test step done", "step", s.name)
	}

	if dump != nil {
		dump.Cleanup()
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
