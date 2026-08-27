package restore

import (
	"context"
	"testing"
)

func envForRun() map[string]string {
	return map[string]string{
		"WORKLOAD": "misskey",
		"S3_URI":   "https://s3.example.com/backups/misskey",
		"DB_HOST":  "db",
		"DB_PORT":  "5432",
		"DB_USER":  "misskey",
		"DB_PASS":  "secret",
	}
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

	if err := Run(context.Background(), nil); err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
}
