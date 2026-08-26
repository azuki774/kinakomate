package restore

import (
	"context"
	"testing"
)

func TestRun(t *testing.T) {
	if err := Run(context.Background(), []string{"-config", "test.yaml"}); err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}
}
