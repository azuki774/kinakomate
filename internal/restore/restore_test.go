package restore

import (
	"context"
	"testing"

	"github.com/azuki774/kinakomate/internal/config"
)

func envForRun() map[string]string {
	return map[string]string{
		"WEB_WORKLOAD": "misskey-web",
		"DB_WORKLOAD":  "misskey-db-v18",
		"S3_REGION":    "us-east-1",
		"S3_BUCKET":    "backups",
		"S3_KEY":       "misskey/daily/dump.sql.gz",
		"DB_HOST":      "db",
		"DB_PORT":      "5432",
		"DB_USER":      "misskey",
		"DB_PASS":      "secret",
	}
}

// noopRunner returns a runner wired to recordingNoop deps so Run exercises the
// workflow without touching real S3 or Kubernetes or PostgreSQL.
func noopRunner(_ context.Context, _ *config.Config) (*runner, error) {
	dep := &recordingDep{}
	return &runner{db: dep, s3: dep, k8s: dep, chk: dep}, nil
}

func TestRun_PreFlightFailsWithoutInput(t *testing.T) {
	// No env set; validation must fail before touching anything.
	if err := Run(context.Background(), nil); err == nil {
		t.Fatal("expected Run to fail when required input is missing")
	}
}

func TestRun_PreFlightPassesWithValidInput(t *testing.T) {
	for k, v := range envForRun() {
		t.Setenv(k, v)
	}

	// Substitute a faked runner so the workflow is exercised without a real
	// Kubernetes cluster or S3 bucket / PostgreSQL server.
	orig := runnerFactory
	t.Cleanup(func() { runnerFactory = orig })
	runnerFactory = noopRunner

	if err := Run(context.Background(), nil); err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
}
