package restore

import (
	"context"
	"fmt"

	"github.com/azuki774/kinakomate/internal/config"
	"github.com/azuki774/kinakomate/internal/log"
)

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
// The scale / readiness control (issue #4) fills this in later. The runner
// operates only on the single fixed workload from the config.
type Kubernetes interface {
	// CheckConnection verifies the runner can reach the Kubernetes API.
	CheckConnection(ctx context.Context, cfg *config.Config) error
	// Scale sets the replica count of the configured workload.
	Scale(ctx context.Context, cfg *config.Config, replicas int) error
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

// newRunner builds a runner with the placeholder no-op dependencies. Each
// dependency's real logic is implemented in later issues; until then every
// step is a no-op that succeeds, so the workflow ordering itself can be
// exercised end to end.
func newRunner() *runner {
	return &runner{
		db:  databaseTODO{},
		s3:  objectStorageTODO{},
		k8s: kubernetesTODO{},
		chk: checksTODO{},
	}
}

// run executes the restore-test workflow in order:
//
//  1. DB connection check
//  2. S3 connection check
//  3. Kubernetes API connection check
//  4. S3 download + decompress
//  5. scale workload to 0
//  6. DB restore
//  7. scale workload to 1
//  8. checks
//
// On the first error it stops immediately and returns the wrapped error; the
// remaining steps are not executed. If the restore (step 6) fails, the workload
// is intentionally left scaled to 0 — the runner does not auto-recover.
func (r *runner) run(ctx context.Context, cfg *config.Config) error {
	logger := log.New()

	type step struct {
		name string
		fn   func(context.Context, *config.Config) error
	}

	steps := []step{
		{"db connection check", r.db.CheckConnection},
		{"s3 connection check", r.s3.CheckConnection},
		{"kubernetes api connection check", r.k8s.CheckConnection},
		{"s3 download + decompress", r.s3.DownloadAndExtract},
		{"scale to 0", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.Scale(ctx, cfg, 0)
		}},
		{"db restore", r.db.Restore},
		{"scale to 1", func(ctx context.Context, cfg *config.Config) error {
			return r.k8s.Scale(ctx, cfg, 1)
		}},
		{"checks", r.chk.Run},
	}

	for _, s := range steps {
		logger.InfoContext(ctx, "restore-test step start", "step", s.name)
		if err := s.fn(ctx, cfg); err != nil {
			return fmt.Errorf("restore-test step %q failed: %w", s.name, err)
		}
		logger.InfoContext(ctx, "restore-test step done", "step", s.name)
	}

	return nil
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

// kubernetesTODO is a placeholder Kubernetes dependency. Real logic lands in issue #4.
type kubernetesTODO struct{}

func (kubernetesTODO) CheckConnection(_ context.Context, _ *config.Config) error {
	// TODO: implement Kubernetes API connection check.
	return nil
}

func (kubernetesTODO) Scale(_ context.Context, _ *config.Config, _ int) error {
	// TODO: implement workload scale.
	return nil
}

// checksTODO is a placeholder Checks dependency. Real logic lands in issue #7.
type checksTODO struct{}

func (checksTODO) Run(_ context.Context, _ *config.Config) error {
	// TODO: implement verification checks.
	return nil
}
