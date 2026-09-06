package restore

import (
	"errors"
	"fmt"
	"time"
)

type phaseName string

const (
	phasePreflight phaseName = "preflight"
	phasePrepare   phaseName = "prepare"
	phaseRestore   phaseName = "restore"
	phaseVerify    phaseName = "verify"
	phaseCleanup   phaseName = "cleanup"
)

var phaseOrder = [...]phaseName{
	phasePreflight,
	phasePrepare,
	phaseRestore,
	phaseVerify,
	phaseCleanup,
}

type phaseStatus string

const (
	phaseStatusSuccess phaseStatus = "success"
	phaseStatusFailure phaseStatus = "failure"
	phaseStatusSkipped phaseStatus = "skipped"
)

type phaseReport struct {
	Phase      phaseName   `json:"phase"`
	Status     phaseStatus `json:"status"`
	DurationMS int64       `json:"duration_ms"`
	Duration   string      `json:"duration"`
}

type objectReport struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	ETag   string `json:"etag"`
	Size   int64  `json:"size"`
}

type finalReport struct {
	Result          string        `json:"result"`
	FailedPhase     string        `json:"failed_phase,omitempty"`
	Error           string        `json:"error,omitempty"`
	RecoveryStatus  string        `json:"recovery_status,omitempty"`
	RecoveryError   string        `json:"recovery_error,omitempty"`
	TotalDurationMS int64         `json:"total_duration_ms"`
	TotalDuration   string        `json:"total_duration"`
	Phases          []phaseReport `json:"phases"`
	Object          *objectReport `json:"object,omitempty"`
}

type phaseSummary struct {
	status   phaseStatus
	duration time.Duration
}

type executionResult struct {
	startAt           time.Time
	currentPhase      phaseName
	currentPhaseStart time.Time
	nextPhase         int
	initialized       bool
	failedPhase       phaseName
	firstErr          error
	recoveryErr       error
	recoveryStatus    string
	recoveryAttempted bool
	phases            map[phaseName]phaseSummary
	object            *objectReport
}

func newExecutionResult(start time.Time) *executionResult {
	return &executionResult{
		startAt: start,
		phases:  make(map[phaseName]phaseSummary, len(phaseOrder)),
	}
}

// beginPhase starts the next normal phase. Cleanup may also be started once as
// recovery after an initialized phase has failed.
func (r *executionResult) beginPhase(phase phaseName, at time.Time) error {
	if phaseIndex(phase) < 0 {
		return fmt.Errorf("unknown phase %q", phase)
	}
	if r.currentPhase != "" {
		return fmt.Errorf("phase %q is already active", r.currentPhase)
	}

	if r.failedPhase != "" {
		if phase != phaseCleanup || !r.recoveryRequired() {
			return fmt.Errorf("cannot begin phase %q after failure in %q", phase, r.failedPhase)
		}
		r.recoveryAttempted = true
	} else if phaseIndex(phase) != r.nextPhase {
		return fmt.Errorf("phase %q is out of order", phase)
	}

	r.currentPhase = phase
	r.currentPhaseStart = at
	return nil
}

func (r *executionResult) completePhase(at time.Time) error {
	if r.currentPhase == "" {
		return fmt.Errorf("no phase is active")
	}

	phase := r.currentPhase
	recovery := r.recoveryAttempted && phase == phaseCleanup
	if recovery {
		r.recoveryStatus = "success"
	} else {
		r.phases[phase] = phaseSummary{
			status:   phaseStatusSuccess,
			duration: elapsed(r.currentPhaseStart, at),
		}
	}
	r.currentPhase = ""
	r.currentPhaseStart = time.Time{}
	if !recovery && phaseIndex(phase) == r.nextPhase {
		r.nextPhase++
	}
	return nil
}

func (r *executionResult) failPhase(phase phaseName, at time.Time, err error) error {
	if r.currentPhase != phase {
		return fmt.Errorf("phase %q is not active", phase)
	}

	recovery := r.recoveryAttempted && phase == phaseCleanup
	if !recovery {
		r.phases[phase] = phaseSummary{
			status:   phaseStatusFailure,
			duration: elapsed(r.currentPhaseStart, at),
		}
	}
	r.currentPhase = ""
	r.currentPhaseStart = time.Time{}
	if recovery {
		r.recoveryStatus = "error"
		r.recordRecoveryError(err)
		return nil
	}
	if r.failedPhase == "" {
		r.failedPhase = phase
		r.firstErr = err
		return nil
	}
	return fmt.Errorf("phase %q already failed", r.failedPhase)
}

func (r *executionResult) failCompletedPhase(phase phaseName, err error) {
	if r.failedPhase != "" {
		r.recordRecoveryError(err)
		return
	}
	r.failedPhase = phase
	r.firstErr = err
	summary := r.phases[phase]
	summary.status = phaseStatusFailure
	r.phases[phase] = summary
}

func (r *executionResult) recordRecoveryError(err error) {
	r.recoveryStatus = "error"
	r.recoveryErr = errors.Join(r.recoveryErr, err)
}

func (r *executionResult) markInitialized() {
	r.initialized = true
}

func (r *executionResult) recoveryRequired() bool {
	return r.initialized && r.failedPhase != "" && r.failedPhase != phasePreflight && !r.recoveryAttempted
}

func (r *executionResult) setObject(dump *Dump) {
	if dump == nil {
		r.object = nil
		return
	}
	r.object = &objectReport{
		Bucket: dump.Bucket,
		Key:    dump.Key,
		ETag:   dump.ETag,
		Size:   dump.Size,
	}
}

func (r *executionResult) report(at time.Time) finalReport {
	report := finalReport{
		Result:          "incomplete",
		TotalDurationMS: durationMilliseconds(elapsed(r.startAt, at)),
		TotalDuration:   elapsed(r.startAt, at).String(),
		Phases:          make([]phaseReport, 0, len(phaseOrder)),
		Object:          r.object,
	}
	if r.failedPhase != "" {
		report.Result = "failure"
		report.FailedPhase = string(r.failedPhase)
		if r.firstErr != nil {
			report.Error = r.firstErr.Error()
		}
		report.RecoveryStatus = r.recoveryStatus
		if r.recoveryErr != nil {
			report.RecoveryError = r.recoveryErr.Error()
		}
	} else if r.currentPhase == "" && r.nextPhase == len(phaseOrder) {
		report.Result = "success"
	}
	for _, phase := range phaseOrder {
		summary, ok := r.phases[phase]
		if !ok {
			summary = phaseSummary{status: phaseStatusSkipped}
		}
		ms, duration := durationFields(summary.duration)
		report.Phases = append(report.Phases, phaseReport{
			Phase:      phase,
			Status:     summary.status,
			DurationMS: ms,
			Duration:   duration,
		})
	}
	return report
}

func phaseIndex(phase phaseName) int {
	for i, candidate := range phaseOrder {
		if candidate == phase {
			return i
		}
	}
	return -1
}

func elapsed(start, end time.Time) time.Duration {
	d := end.Sub(start)
	if d < 0 {
		return 0
	}
	return d
}

func durationMilliseconds(d time.Duration) int64 {
	return int64(d / time.Millisecond)
}

func durationFields(d time.Duration) (int64, string) {
	return durationMilliseconds(d), d.String()
}
