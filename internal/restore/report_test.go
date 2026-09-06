package restore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/azuki774/kinakomate/internal/config"
	"github.com/azuki774/kinakomate/internal/log"
)

func TestRunFinalReport_Success(t *testing.T) {
	setRunEnv(t)
	dep := &recordingDep{}
	var output bytes.Buffer
	setRunnerFactory(t, dep)

	if err := runWithLogger(context.Background(), nil, log.NewWithWriter(&output)); err != nil {
		t.Fatalf("runWithLogger returned unexpected error: %v", err)
	}

	report := decodeFinalReport(t, &output)
	if report.Result != "success" {
		t.Fatalf("result = %q, want success", report.Result)
	}
	if len(report.Phases) != len(phaseOrder) {
		t.Fatalf("phases = %+v, want %d phases", report.Phases, len(phaseOrder))
	}
	for _, phase := range report.Phases {
		if phase.Status != phaseStatusSuccess {
			t.Errorf("phase %q status = %q, want success", phase.Phase, phase.Status)
		}
	}
	if !strings.Contains(output.String(), `"phase":"restore","step":"wait db replicas 1"`) {
		t.Fatalf("DB readiness wait was not logged in the restore phase: %q", output.String())
	}
	if report.Object == nil {
		t.Fatal("success report has no object metadata")
	}
	if report.Object.Bucket != "backups" || report.Object.Key != "daily.sql.gz" || report.Object.ETag != "\"etag\"" || report.Object.Size != 42 {
		t.Fatalf("object = %+v, want backup metadata", report.Object)
	}
	if strings.Contains(output.String(), "secret") {
		t.Fatalf("report logs leaked the database password: %q", output.String())
	}
}

func TestRunFinalReport_ResetFailureRecoversWebOnly(t *testing.T) {
	setRunEnv(t)
	dep := &recordingDep{failOn: map[string]error{"db-reset": errors.New("reset boom")}}
	var output bytes.Buffer
	setRunnerFactory(t, dep)

	if err := runWithLogger(context.Background(), nil, log.NewWithWriter(&output)); err == nil {
		t.Fatal("runWithLogger succeeded, want reset failure")
	}

	report := decodeFinalReport(t, &output)
	if report.Result != "failure" || report.FailedPhase != string(phaseRestore) {
		t.Fatalf("failure report = %+v, want restore failure", report)
	}
	if !strings.Contains(report.Error, "reset boom") {
		t.Fatalf("error = %q, want reset boom", report.Error)
	}
	if got := report.Phases[phaseIndexForTest(phasePrepare)].Status; got != phaseStatusSuccess {
		t.Fatalf("prepare status = %q, want success", got)
	}
	if got := report.Phases[phaseIndexForTest(phaseRestore)].Status; got != phaseStatusFailure {
		t.Fatalf("restore status = %q, want failure", got)
	}
	if got := report.Phases[phaseIndexForTest(phaseVerify)].Status; got != phaseStatusSkipped {
		t.Fatalf("verify status = %q, want skipped", got)
	}
	if got := report.Phases[phaseIndexForTest(phaseCleanup)].Status; got != phaseStatusSkipped {
		t.Fatalf("cleanup status = %q, want skipped because recovery is separate", got)
	}
	if report.RecoveryStatus != "success" {
		t.Fatalf("recovery_status = %q, want success", report.RecoveryStatus)
	}
	for _, call := range dep.calls {
		if call == "scale:misskey-db-v18:0" {
			t.Fatalf("failure recovery scaled the database to zero: calls = %v", dep.calls)
		}
	}
}

func TestRunFinalReport_PreflightFailureSkipsWorkloads(t *testing.T) {
	for key := range envForRun() {
		t.Setenv(key, "")
	}
	var output bytes.Buffer
	called := false
	original := runnerFactory
	t.Cleanup(func() { runnerFactory = original })
	runnerFactory = func(context.Context, *config.Config, *slog.Logger) (*runner, error) {
		called = true
		return nil, errors.New("runner must not be constructed")
	}

	if err := runWithLogger(context.Background(), nil, log.NewWithWriter(&output)); err == nil {
		t.Fatal("runWithLogger succeeded, want preflight failure")
	}

	report := decodeFinalReport(t, &output)
	if report.Result != "failure" || report.FailedPhase != string(phasePreflight) {
		t.Fatalf("preflight report = %+v, want preflight failure", report)
	}
	if !strings.Contains(report.Error, "required input") {
		t.Fatalf("error = %q, want required input validation error", report.Error)
	}
	if called {
		t.Fatal("runnerFactory was called after preflight failure")
	}
	for _, phase := range report.Phases[1:] {
		if phase.Status != phaseStatusSkipped {
			t.Errorf("phase %q status = %q, want skipped", phase.Phase, phase.Status)
		}
	}
}

func TestRunFinalReport_InvalidFlagsAreStructured(t *testing.T) {
	setRunEnv(t)
	var output bytes.Buffer
	setRunnerFactory(t, &recordingDep{})

	if err := runWithLogger(context.Background(), []string{"-unknown"}, log.NewWithWriter(&output)); err == nil {
		t.Fatal("runWithLogger succeeded, want flag parsing failure")
	}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("flag parsing emitted non-JSON output: %v; line = %q", err, line)
		}
	}
	if report := decodeFinalReport(t, &output); report.FailedPhase != string(phasePreflight) {
		t.Fatalf("report = %+v, want preflight failure", report)
	}
}

func setRunEnv(t *testing.T) {
	t.Helper()
	for key, value := range envForRun() {
		t.Setenv(key, value)
	}
}

func setRunnerFactory(t *testing.T, dep *recordingDep) {
	t.Helper()
	original := runnerFactory
	t.Cleanup(func() { runnerFactory = original })
	runnerFactory = func(_ context.Context, _ *config.Config, logger *slog.Logger) (*runner, error) {
		return &runner{db: dep, s3: dep, k8s: dep, api: dep, logger: logger}, nil
	}
}

func decodeFinalReport(t *testing.T, output *bytes.Buffer) finalReport {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatalf("logger emitted no records")
	}
	var report finalReport
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &report); err != nil {
		t.Fatalf("unmarshal final report: %v; output = %q", err, output.String())
	}
	return report
}

func phaseIndexForTest(phase phaseName) int {
	for i, candidate := range phaseOrder {
		if candidate == phase {
			return i
		}
	}
	return -1
}
