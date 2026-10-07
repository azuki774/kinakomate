package backup

import (
	"errors"
	"time"
)

var phaseNames = [...]string{"preflight", "dump", "validate", "upload", "cleanup"}

var phaseErrors = [...]error{
	errors.New("backup preflight failed"),
	errors.New("backup dump failed"),
	errors.New("backup validation failed"),
	errors.New("backup upload failed"),
	errors.New("backup cleanup failed"),
}

// RunSummary contains only operational metrics safe to pass to notifications.
type RunSummary struct {
	FailedPhase         string
	BackupSize          int64
	BackupSizeAvailable bool
	UploadVerified      bool
	Total               time.Duration
	Phases              []PhaseSummary
}

// PhaseSummary describes one backup operation phase.
type PhaseSummary struct {
	Name     string
	Status   string
	Duration time.Duration
}

type runResult struct {
	started time.Time
	summary RunSummary
	err     error
}

func newRunResult(started time.Time) *runResult {
	phases := make([]PhaseSummary, len(phaseNames))
	for i, name := range phaseNames {
		phases[i] = PhaseSummary{Name: name, Status: "skipped"}
	}
	return &runResult{
		started: started,
		summary: RunSummary{Phases: phases},
	}
}

func (r *runResult) runPhase(index int, fn func() error) bool {
	started := time.Now()
	err := fn()
	phase := &r.summary.Phases[index]
	phase.Duration = time.Since(started)
	if err == nil {
		phase.Status = "success"
		return true
	}

	phase.Status = "failure"
	if r.err == nil {
		r.summary.FailedPhase = phase.Name
		r.err = phaseErrors[index]
	}
	return false
}

func (r *runResult) finish() (RunSummary, error) {
	r.summary.Total = time.Since(r.started)
	return r.summary, r.err
}
