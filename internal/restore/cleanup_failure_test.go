package restore

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/azuki774/kinakomate/internal/config"
	"github.com/azuki774/kinakomate/internal/log"
)

func TestRunFinalReport_CleanupFailurePreservesFailureAfterWebRecovery(t *testing.T) {
	setRunEnv(t)
	dep := &recordingDep{
		scaleErrors: map[string][]error{
			"scale:misskey-web:0": {nil, errors.New("cleanup web down"), nil},
		},
	}
	var output bytes.Buffer
	original := runnerFactory
	t.Cleanup(func() { runnerFactory = original })
	runnerFactory = func(_ context.Context, _ *config.Config, logger *slog.Logger) (*runner, error) {
		return &runner{db: dep, s3: dep, k8s: dep, api: dep, logger: logger}, nil
	}

	if err := runWithLogger(context.Background(), nil, log.NewWithWriter(&output)); err == nil {
		t.Fatal("runWithLogger succeeded, want cleanup failure")
	}

	report := decodeFinalReport(t, &output)
	if report.Result != "failure" || report.FailedPhase != string(phaseCleanup) {
		t.Fatalf("report = %+v, want cleanup failure", report)
	}
	if !strings.Contains(report.Error, "cleanup web down") {
		t.Fatalf("error = %q, want cleanup web down", report.Error)
	}
	if got := report.Phases[phaseIndexForTest(phaseCleanup)].Status; got != phaseStatusFailure {
		t.Fatalf("cleanup status = %q, want failure", got)
	}
	if report.RecoveryStatus != "success" {
		t.Fatalf("recovery_status = %q, want success", report.RecoveryStatus)
	}
	if !strings.Contains(output.String(), `"msg":"rollback: scaled web to 0"`) {
		t.Fatalf("recovery success was not logged: %q", output.String())
	}
	for _, call := range dep.calls {
		if call == "scale:misskey-db-v18:0" {
			t.Fatalf("recovery scaled the database to zero: calls = %v", dep.calls)
		}
	}
}

func TestRunFinalReport_DumpCleanupFailureFailsSuccessfulRun(t *testing.T) {
	setRunEnv(t)
	cleanupCalls := 0
	dep := &recordingDep{
		dumpPath: "injected-dump",
		dumpCleanup: func() error {
			cleanupCalls++
			return errors.New("dump cleanup failed")
		},
	}
	var output bytes.Buffer
	setRunnerFactory(t, dep)

	err := runWithLogger(context.Background(), nil, log.NewWithWriter(&output))
	if err == nil {
		t.Fatal("runWithLogger succeeded despite dump cleanup failure")
	}

	report := decodeFinalReport(t, &output)
	if report.Result != "failure" || report.FailedPhase != string(phaseCleanup) {
		t.Fatalf("report = %+v, want cleanup failure", report)
	}
	if !strings.Contains(report.Error, "dump cleanup failed") {
		t.Fatalf("error = %q, want dump cleanup failure", report.Error)
	}
	if got := report.Phases[phaseIndexForTest(phaseCleanup)].Status; got != phaseStatusFailure {
		t.Fatalf("cleanup status = %q, want failure", got)
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", cleanupCalls)
	}
	if !strings.Contains(output.String(), `"msg":"staged dump cleanup failed"`) {
		t.Fatalf("cleanup failure was not logged: %q", output.String())
	}
}

func TestRunFinalReport_DumpCleanupFailurePreservesWorkflowError(t *testing.T) {
	setRunEnv(t)
	dep := &recordingDep{
		failOn:   map[string]error{"db-reset": errors.New("reset boom")},
		dumpPath: "injected-dump",
		dumpCleanup: func() error {
			return errors.New("dump cleanup failed")
		},
	}
	var output bytes.Buffer
	setRunnerFactory(t, dep)

	err := runWithLogger(context.Background(), nil, log.NewWithWriter(&output))
	if err == nil {
		t.Fatal("runWithLogger succeeded despite reset failure")
	}
	if !strings.Contains(err.Error(), "reset boom") || strings.Contains(err.Error(), "dump cleanup failed") {
		t.Fatalf("returned error = %q, want original reset error only", err)
	}

	report := decodeFinalReport(t, &output)
	if !strings.Contains(report.Error, "reset boom") || strings.Contains(report.Error, "dump cleanup failed") {
		t.Fatalf("error = %q, want original reset error only", report.Error)
	}
	if report.RecoveryStatus != "error" || !strings.Contains(report.RecoveryError, "dump cleanup failed") {
		t.Fatalf("recovery = %q/%q, want dump cleanup failure", report.RecoveryStatus, report.RecoveryError)
	}
}
