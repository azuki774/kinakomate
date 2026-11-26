package federation

import (
	"errors"
	"time"
)

var phaseNames = [...]string{"preflight", "fetch", "write"}

var phaseErrors = [...]error{
	errors.New("federation-report preflight failed"),
	errors.New("federation-report fetch failed"),
	errors.New("federation-report write failed"),
}

// RunSummary contains only operational metrics and the local output path, so
// it is safe to log or report.
type RunSummary struct {
	FailedPhase             string
	OutputPath              string
	TotalInstances          int
	ActiveInstances         int
	NotRespondingInstances  int
	SuspendedInstances      int
	BlockedInstances        int
	NewInstancesCount       int
	UnhealthyInstancesCount int
	UnhealthyWithFFCount    int
	CompletedAt             time.Time
	Total                   time.Duration
	Phases                  []PhaseSummary
}

// PhaseSummary describes one federation-report phase.
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
	completedAt := time.Now()
	r.summary.CompletedAt = completedAt
	r.summary.Total = completedAt.Sub(r.started)
	return r.summary, r.err
}
