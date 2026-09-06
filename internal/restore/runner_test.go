package restore

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
)

// recordingDep records every call so tests can assert on ordering and which
// steps ran. It implements all four runner interfaces.
type recordingDep struct {
	calls         []string
	failOn        map[string]error
	scaleReplicas []int
	waitTimeouts  []time.Duration
	apiTimeout    time.Duration
}

func (d *recordingDep) CheckConnection(_ context.Context, _ *config.Config) error {
	d.calls = append(d.calls, "check")
	return d.failOn["check"]
}

func (d *recordingDep) DownloadAndExtract(_ context.Context, _ *config.Config) (*Dump, error) {
	d.calls = append(d.calls, "s3-download")
	return d.dump(), d.failOn["s3-download"]
}

func (d *recordingDep) Reset(_ context.Context, _ *config.Config) error {
	d.calls = append(d.calls, "db-reset")
	return d.failOn["db-reset"]
}

func (d *recordingDep) Restore(_ context.Context, _ *config.Config, dump *Dump) error {
	if dump == nil {
		return errors.New("restore called without a dump")
	}
	d.calls = append(d.calls, "db-restore")
	return d.failOn["db-restore"]
}

func (d *recordingDep) GetReplicas(_ context.Context, _ *config.Config, workload string) (int, error) {
	d.calls = append(d.calls, "getreplicas:"+workload)
	if err := d.failOn["getreplicas:"+workload]; err != nil {
		return 0, err
	}
	return 0, nil
}

func (d *recordingDep) Scale(_ context.Context, _ *config.Config, workload string, replicas int) error {
	d.scaleReplicas = append(d.scaleReplicas, replicas)
	d.calls = append(d.calls, "scale:"+workload+":"+itoa(replicas))
	return d.failOn["scale:"+workload+":"+itoa(replicas)]
}

func (d *recordingDep) WaitForReplicas(_ context.Context, _ *config.Config, workload string, want int, timeout time.Duration) error {
	d.calls = append(d.calls, "wait:"+workload+":"+itoa(want))
	d.waitTimeouts = append(d.waitTimeouts, timeout)
	return d.failOn["wait:"+workload+":"+itoa(want)]
}

func (d *recordingDep) WaitForReadiness(_ context.Context, _ *config.Config, timeout time.Duration) error {
	d.calls = append(d.calls, "misskey-readiness")
	d.apiTimeout = timeout
	return d.failOn["misskey-readiness"]
}

func (d *recordingDep) CheckGlobalTimeline(_ context.Context, _ *config.Config) error {
	d.calls = append(d.calls, "misskey-global-timeline")
	return d.failOn["misskey-global-timeline"]
}

// dump returns a Dump backed by a real temp file so Dump.Cleanup behaves.
func (d *recordingDep) dump() *Dump {
	f, err := os.CreateTemp("", "kinakomate-dump-*.sql.gz")
	if err != nil {
		return &Dump{Path: ""}
	}
	f.Close() //nolint:errcheck
	return &Dump{Path: f.Name(), Bucket: "b", Key: "k"}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	if n == 1 {
		return "1"
	}
	return "x"
}

func testConfig() *config.Config {
	return &config.Config{
		WebWorkload:    "misskey-web",
		DBWorkload:     "misskey-db-v18",
		DBName:         config.DBName,
		MisskeyBaseURL: "https://misskey.example",
	}
}

func TestRunnerRun_Order(t *testing.T) {
	dep := &recordingDep{}
	r := &runner{db: dep, s3: dep, k8s: dep, api: dep}

	if err := r.run(context.Background(), testConfig()); err != nil {
		t.Fatalf("run returned unexpected error: %v", err)
	}

	want := []string{
		"getreplicas:misskey-web",
		"getreplicas:misskey-db-v18",
		"check",
		"check",
		"check",
		"s3-download",
		"scale:misskey-web:0",
		"scale:misskey-db-v18:1",
		"wait:misskey-web:0",
		"wait:misskey-db-v18:1",
		"db-reset",
		"db-restore",
		"scale:misskey-web:1",
		"wait:misskey-web:1",
		"misskey-readiness",
		"misskey-global-timeline",
		"scale:misskey-web:0",
		"scale:misskey-db-v18:0",
	}
	if len(dep.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", dep.calls, want)
	}
	for i := range want {
		if dep.calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q (full: %v)", i, dep.calls[i], want[i], dep.calls)
		}
	}

	// Scale transitions in order: web 0, db 1, web 1, cleanup web 0, cleanup db 0.
	wantReplicas := []int{0, 1, 1, 0, 0}
	if len(dep.scaleReplicas) != len(wantReplicas) {
		t.Fatalf("scaleReplicas = %v, want %v", dep.scaleReplicas, wantReplicas)
	}
	for i := range wantReplicas {
		if dep.scaleReplicas[i] != wantReplicas[i] {
			t.Fatalf("scaleReplicas[%d] = %d, want %d (full: %v)", i, dep.scaleReplicas[i], wantReplicas[i], dep.scaleReplicas)
		}
	}

	if len(dep.waitTimeouts) != 3 {
		t.Fatalf("waitTimeouts = %v, want three waits", dep.waitTimeouts)
	}
	for i, timeout := range dep.waitTimeouts {
		if timeout != scaleTimeout {
			t.Fatalf("waitTimeouts[%d] = %v, want %v", i, timeout, scaleTimeout)
		}
	}
	if dep.apiTimeout != scaleTimeout {
		t.Fatalf("apiTimeout = %v, want %v", dep.apiTimeout, scaleTimeout)
	}
}

func TestRunnerRun_StopsOnResetFailure(t *testing.T) {
	dep := &recordingDep{failOn: map[string]error{"db-reset": errors.New("reset boom")}}
	r := &runner{db: dep, s3: dep, k8s: dep, api: dep}

	err := r.run(context.Background(), testConfig())
	if err == nil {
		t.Fatal("expected run to fail when database reset fails")
	}
	if !strings.Contains(err.Error(), "reset database") {
		t.Fatalf("error = %v, want it to mention reset database", err)
	}

	// After a reset failure the restore must never run; the web must be rolled
	// back to 0 (via deferred rollback) and scale-to-1, API checks, and cleanup
	// must never run.
	for _, c := range dep.calls {
		if c == "db-restore" || c == "scale:misskey-web:1" || c == "misskey-readiness" || c == "misskey-global-timeline" || c == "scale:misskey-db-v18:0" {
			t.Fatalf("calls = %v, unexpected call %q after reset failure", dep.calls, c)
		}
	}
}

func TestRunnerRun_StopsOnDBReadinessFailure(t *testing.T) {
	dep := &recordingDep{failOn: map[string]error{
		"wait:misskey-db-v18:1": errors.New("db not ready"),
	}}
	r := &runner{db: dep, s3: dep, k8s: dep, api: dep}

	err := r.run(context.Background(), testConfig())
	if err == nil {
		t.Fatal("expected run to fail when DB readiness wait fails")
	}
	if !strings.Contains(err.Error(), "wait db replicas 1") {
		t.Fatalf("error = %v, want it to mention DB readiness", err)
	}
	for _, c := range dep.calls {
		if c == "db-reset" || c == "db-restore" {
			t.Fatalf("calls = %v, database operations must not run before DB readiness", dep.calls)
		}
	}
}

func TestRunnerRun_StopsOnRestoreFailure(t *testing.T) {
	dep := &recordingDep{failOn: map[string]error{"db-restore": errors.New("restore boom")}}
	r := &runner{db: dep, s3: dep, k8s: dep, api: dep}

	err := r.run(context.Background(), testConfig())
	if err == nil {
		t.Fatal("expected run to fail when restore fails")
	}
	if !strings.Contains(err.Error(), "db restore") {
		t.Fatalf("error = %v, want it to mention db restore", err)
	}

	// After a restore failure the web must be rolled back to 0 (via deferred
	// rollback) but scale-to-1, API checks, and cleanup must never run.
	for _, c := range dep.calls {
		if c == "scale:misskey-web:1" || c == "misskey-readiness" || c == "misskey-global-timeline" || c == "scale:misskey-db-v18:0" {
			t.Fatalf("calls = %v, unexpected call %q after restore failure", dep.calls, c)
		}
	}

	// Scale transitions: web 0 (before restore), db 1 (before restore),
	// then the deferred rollback scales web to 0 again.
	wantReplicas := []int{0, 1, 0}
	if len(dep.scaleReplicas) != len(wantReplicas) {
		t.Fatalf("scaleReplicas = %v, want %v", dep.scaleReplicas, wantReplicas)
	}
	for i := range wantReplicas {
		if dep.scaleReplicas[i] != wantReplicas[i] {
			t.Fatalf("scaleReplicas[%d] = %d, want %d (full: %v)", i, dep.scaleReplicas[i], wantReplicas[i], dep.scaleReplicas)
		}
	}
}

func TestRunnerRun_StopsOnMisskeyReadinessFailure(t *testing.T) {
	dep := &recordingDep{failOn: map[string]error{
		"misskey-readiness": errors.New("Misskey not ready"),
	}}
	r := &runner{db: dep, s3: dep, k8s: dep, api: dep}

	err := r.run(context.Background(), testConfig())
	if err == nil {
		t.Fatal("expected run to fail when Misskey readiness fails")
	}
	if !strings.Contains(err.Error(), "wait for Misskey API readiness") {
		t.Fatalf("error = %v, want it to mention Misskey readiness", err)
	}

	wantTail := []string{
		"scale:misskey-web:1",
		"wait:misskey-web:1",
		"misskey-readiness",
		"scale:misskey-web:0",
	}
	assertCallTail(t, dep.calls, wantTail)
	assertNoCalls(t, dep.calls, "misskey-global-timeline", "scale:misskey-db-v18:0")
}

func TestRunnerRun_StopsOnWebStartWaitFailure(t *testing.T) {
	dep := &recordingDep{failOn: map[string]error{
		"wait:misskey-web:1": errors.New("web not ready"),
	}}
	r := &runner{db: dep, s3: dep, k8s: dep, api: dep}

	err := r.run(context.Background(), testConfig())
	if err == nil {
		t.Fatal("expected run to fail when the web replica wait fails")
	}
	if !strings.Contains(err.Error(), "wait web replicas 1") {
		t.Fatalf("error = %v, want it to mention the web replica wait", err)
	}

	assertCallTail(t, dep.calls, []string{
		"db-restore",
		"scale:misskey-web:1",
		"wait:misskey-web:1",
		"scale:misskey-web:0",
	})
	assertNoCalls(t, dep.calls, "misskey-readiness", "misskey-global-timeline", "scale:misskey-db-v18:0")
}

func TestRunnerRun_StopsOnGlobalTimelineFailure(t *testing.T) {
	dep := &recordingDep{failOn: map[string]error{
		"misskey-global-timeline": errors.New("invalid timeline"),
	}}
	r := &runner{db: dep, s3: dep, k8s: dep, api: dep}

	err := r.run(context.Background(), testConfig())
	if err == nil {
		t.Fatal("expected run to fail when global timeline check fails")
	}
	if !strings.Contains(err.Error(), "リストアデータのGTL取得確認") {
		t.Fatalf("error = %v, want it to mention global timeline", err)
	}

	wantTail := []string{
		"wait:misskey-web:1",
		"misskey-readiness",
		"misskey-global-timeline",
		"scale:misskey-web:0",
	}
	assertCallTail(t, dep.calls, wantTail)
	assertNoCalls(t, dep.calls, "scale:misskey-db-v18:0")
}

func TestRunnerRun_FailureBeforeDownloadDoesNotPanic(t *testing.T) {
	dep := &recordingDep{failOn: map[string]error{"check": errors.New("connection boom")}}
	r := &runner{db: dep, s3: dep, k8s: dep, api: dep}

	err := r.run(context.Background(), testConfig())
	if err == nil {
		t.Fatal("expected run to fail when a connection check fails")
	}
	if !strings.Contains(err.Error(), "db connection check") {
		t.Fatalf("error = %v, want it to mention DB connection check", err)
	}
	assertCallTail(t, dep.calls, []string{"check", "scale:misskey-web:0"})
	assertNoCalls(t, dep.calls, "s3-download")
}

func assertCallTail(t *testing.T, calls, want []string) {
	t.Helper()
	if len(calls) < len(want) {
		t.Fatalf("calls = %v, want tail %v", calls, want)
	}
	got := calls[len(calls)-len(want):]
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("calls tail = %v, want %v (full: %v)", got, want, calls)
		}
	}
}

func assertNoCalls(t *testing.T, calls []string, unwanted ...string) {
	t.Helper()
	for _, call := range calls {
		for _, wantAbsent := range unwanted {
			if call == wantAbsent {
				t.Fatalf("calls = %v, unexpected call %q", calls, call)
			}
		}
	}
}
