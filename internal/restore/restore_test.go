package restore

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/azuki774/kinakomate/internal/config"
)

func envForRun() map[string]string {
	return map[string]string{
		"WEB_WORKLOAD":     "misskey-web",
		"DB_WORKLOAD":      "misskey-db-v18",
		"S3_REGION":        "us-east-1",
		"S3_ENDPOINT":      "",
		"S3_BUCKET":        "backups",
		"S3_KEY":           "misskey/daily/dump.sql.gz",
		"DB_HOST":          "db",
		"DB_PORT":          "5432",
		"DB_USER":          "misskey",
		"DB_PASS":          "secret",
		"MISSKEY_BASE_URL": "https://misskey.example",
	}
}

func TestRunWithSummary_ReportsDeferredDumpCleanupFailureAndPreservesRunError(t *testing.T) {
	for k, v := range envForRun() {
		t.Setenv(k, v)
	}
	runErr := errors.New("restore failed")
	cleanupErr := errors.New("temporary file cleanup failed")
	orig := runnerFactory
	t.Cleanup(func() { runnerFactory = orig })
	runnerFactory = func(_ context.Context, _ *config.Config, logger *slog.Logger) (*runner, error) {
		dep := &recordingDep{failOn: map[string]error{"db-restore": runErr}, dumpCleanup: func() error { return cleanupErr }}
		return &runner{db: dep, s3: dep, k8s: dep, api: dep, logger: logger}, nil
	}
	summary, err := runWithSummaryLogger(context.Background(), nil, nil)
	if !errors.Is(err, runErr) {
		t.Fatalf("run error = %v, want original restore error", err)
	}
	if summary.TempCleanupStatus != "error" {
		t.Fatalf("cleanup status = %q, want error", summary.TempCleanupStatus)
	}
	if !summary.RecoveryAttempted || summary.RecoveryStatus != "success" {
		t.Fatalf("file deletion failure overwrote recovery outcome: %+v", summary)
	}
}

// noopRunner returns a runner wired to recordingNoop deps so Run exercises the
// workflow without touching real S3 or Kubernetes or PostgreSQL.
func noopRunner(_ context.Context, _ *config.Config, logger *slog.Logger) (*runner, error) {
	dep := &recordingDep{}
	return &runner{db: dep, s3: dep, k8s: dep, api: dep, logger: logger}, nil
}

func TestRun_PreFlightFailsWithoutInput(t *testing.T) {
	for key := range envForRun() {
		t.Setenv(key, "")
	}
	t.Setenv("S3_ENDPOINT", "")

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
