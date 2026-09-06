package restore

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecutionResult_FinalReportContainsPhaseDurations(t *testing.T) {
	result := newExecutionResult(time.Unix(0, 0))
	if err := result.beginPhase(phasePreflight, time.Unix(0, 0)); err != nil {
		t.Fatalf("begin preflight: %v", err)
	}
	if err := result.completePhase(time.Unix(0, int64(1500*time.Millisecond))); err != nil {
		t.Fatalf("complete preflight: %v", err)
	}
	result.markInitialized()
	if err := result.beginPhase(phasePrepare, time.Unix(0, int64(2*time.Second))); err != nil {
		t.Fatalf("begin prepare: %v", err)
	}
	if err := result.completePhase(time.Unix(0, int64(5*time.Second))); err != nil {
		t.Fatalf("complete prepare: %v", err)
	}

	report := result.report(time.Unix(0, int64(7*time.Second)))
	if report.TotalDurationMS != 7000 || report.TotalDuration != "7s" {
		t.Fatalf("report total = %d/%q, want 7000/7s", report.TotalDurationMS, report.TotalDuration)
	}
	if got := report.Phases[0]; got.Status != phaseStatusSuccess || got.DurationMS != 1500 || got.Duration != "1.5s" {
		t.Fatalf("preflight = %+v, want success/1500/1.5s", got)
	}
	if got := report.Phases[1]; got.Status != phaseStatusSuccess || got.DurationMS != 3000 || got.Duration != "3s" {
		t.Fatalf("prepare = %+v, want success/3000/3s", got)
	}
	if got := report.Phases[2]; got.Status != phaseStatusSkipped || got.DurationMS != 0 || got.Duration != "0s" {
		t.Fatalf("restore = %+v, want skipped/0/0s", got)
	}
	if got := report.Phases[3]; got.Status != phaseStatusSkipped || got.DurationMS != 0 {
		t.Fatalf("verify = %+v, want skipped/0", got)
	}
	if got := report.Phases[4]; got.Status != phaseStatusSkipped || got.DurationMS != 0 {
		t.Fatalf("cleanup = %+v, want skipped/0", got)
	}
}

func TestExecutionResult_PreservesPhaseOrder(t *testing.T) {
	result := newExecutionResult(time.Unix(0, 0))
	phases := []phaseName{phasePreflight, phasePrepare, phaseRestore, phaseVerify, phaseCleanup}

	for i, phase := range phases {
		at := time.Unix(0, int64(i)*int64(time.Second))
		if err := result.beginPhase(phase, at); err != nil {
			t.Fatalf("begin %q: %v", phase, err)
		}
		if err := result.completePhase(at.Add(time.Second)); err != nil {
			t.Fatalf("complete %q: %v", phase, err)
		}
	}

	report := result.report(time.Unix(0, 5*int64(time.Second)))
	for i, want := range phases {
		if got := report.Phases[i].Phase; got != want {
			t.Errorf("phase %d = %q, want %q", i, got, want)
		}
		if report.Phases[i].Status != phaseStatusSuccess {
			t.Errorf("phase %d status = %q, want success", i, report.Phases[i].Status)
		}
	}
	if report.Result != "success" {
		t.Fatalf("complete report result = %q, want success", report.Result)
	}
}

func TestExecutionResult_RequiresFixedPhaseOrder(t *testing.T) {
	result := newExecutionResult(time.Unix(0, 0))
	if err := result.beginPhase(phaseRestore, time.Unix(0, 0)); err == nil {
		t.Fatal("begin restore before preflight must fail")
	}

	if err := result.beginPhase(phasePreflight, time.Unix(0, 0)); err != nil {
		t.Fatalf("begin preflight: %v", err)
	}
	if err := result.beginPhase(phasePrepare, time.Unix(0, 0)); err == nil {
		t.Fatal("begin prepare while preflight is active must fail")
	}
	if err := result.completePhase(time.Unix(0, int64(time.Second))); err != nil {
		t.Fatalf("complete preflight: %v", err)
	}
	if err := result.beginPhase(phaseRestore, time.Unix(0, int64(time.Second))); err == nil {
		t.Fatal("begin restore before prepare must fail")
	}
}

func TestExecutionResult_FailureStopsAndRequiresWebRecovery(t *testing.T) {
	result := newExecutionResult(time.Unix(0, 0))
	if err := result.beginPhase(phasePreflight, time.Unix(0, 0)); err != nil {
		t.Fatalf("begin preflight: %v", err)
	}
	if err := result.completePhase(time.Unix(0, 1)); err != nil {
		t.Fatalf("complete preflight: %v", err)
	}
	result.markInitialized()
	if err := result.beginPhase(phasePrepare, time.Unix(0, 1)); err != nil {
		t.Fatalf("begin prepare: %v", err)
	}
	if err := result.failPhase(phasePrepare, time.Unix(0, int64(time.Second)), errors.New("prepare boom")); err != nil {
		t.Fatalf("fail prepare: %v", err)
	}

	if !result.recoveryRequired() {
		t.Fatal("failed initialized run must require web recovery")
	}
	if err := result.beginPhase(phaseVerify, time.Unix(0, int64(2*time.Second))); err == nil {
		t.Fatal("failed run must reject later normal phases")
	}
	if err := result.beginPhase(phaseCleanup, time.Unix(0, int64(2*time.Second))); err != nil {
		t.Fatalf("failed initialized run must allow cleanup recovery: %v", err)
	}
	if err := result.failPhase(phaseCleanup, time.Unix(0, int64(3*time.Second)), errors.New("cleanup boom")); err != nil {
		t.Fatalf("fail cleanup recovery: %v", err)
	}

	report := result.report(time.Unix(0, int64(4*time.Second)))
	if report.FailedPhase != string(phasePrepare) || report.Result != "failure" {
		t.Fatalf("failure report = %+v", report)
	}
	if report.Error != "prepare boom" {
		t.Fatalf("failure error = %q, want prepare boom", report.Error)
	}
	if report.RecoveryStatus != "error" {
		t.Fatalf("recovery status = %q, want error", report.RecoveryStatus)
	}
	if report.RecoveryError != "cleanup boom" {
		t.Fatalf("recovery error = %q, want cleanup boom", report.RecoveryError)
	}
	if got := report.Phases[1]; got.Status != phaseStatusFailure || got.DurationMS != 999 {
		t.Fatalf("prepare = %+v, want failure/999ms", got)
	}
	if got := report.Phases[4]; got.Status != phaseStatusSkipped || got.DurationMS != 0 {
		t.Fatalf("cleanup = %+v, want skipped/0 because recovery is separate", got)
	}
}

func TestExecutionResult_PreflightFailureDoesNotRequireRecovery(t *testing.T) {
	result := newExecutionResult(time.Unix(0, 0))
	if err := result.beginPhase(phasePreflight, time.Unix(0, 0)); err != nil {
		t.Fatalf("begin preflight: %v", err)
	}
	if err := result.failPhase(phasePreflight, time.Unix(0, int64(time.Second)), errors.New("invalid input")); err != nil {
		t.Fatalf("fail preflight: %v", err)
	}
	if result.recoveryRequired() {
		t.Fatal("preflight failure must not scale workloads")
	}
	if err := result.beginPhase(phaseCleanup, time.Unix(0, int64(time.Second))); err == nil {
		t.Fatal("preflight failure must not allow cleanup recovery")
	}
}

func TestExecutionResult_ReportIsIncompleteBeforeCompletion(t *testing.T) {
	start := time.Unix(0, 0)

	unstarted := newExecutionResult(start).report(time.Unix(0, int64(time.Second)))
	if unstarted.Result != "incomplete" {
		t.Fatalf("unstarted report result = %q, want incomplete", unstarted.Result)
	}

	activeResult := newExecutionResult(start)
	if err := activeResult.beginPhase(phasePreflight, start); err != nil {
		t.Fatalf("begin preflight: %v", err)
	}
	active := activeResult.report(time.Unix(0, int64(time.Second)))
	if active.Result != "incomplete" {
		t.Fatalf("active report result = %q, want incomplete", active.Result)
	}
}

func TestExecutionResult_CopiesOnlyDumpMetadata(t *testing.T) {
	result := newExecutionResult(time.Unix(0, 0))
	dump := &Dump{
		Path:   "/tmp/private-dump.sql.gz",
		Bucket: "backups",
		Key:    "daily.sql.gz",
		ETag:   "\"etag\"",
		Size:   42,
	}
	result.setObject(dump)
	dump.Bucket = "changed"

	report := result.report(time.Unix(0, 0))
	if report.Object == nil {
		t.Fatal("report object is nil")
	}
	if report.Object.Bucket != "backups" || report.Object.Key != "daily.sql.gz" || report.Object.ETag != "\"etag\"" || report.Object.Size != 42 {
		t.Fatalf("object = %+v, want copied dump metadata", report.Object)
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if strings.Contains(string(data), "private-dump.sql.gz") || strings.Contains(string(data), "path") {
		t.Fatalf("report leaked dump path: %s", data)
	}
}
