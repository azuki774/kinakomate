package restore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
	"github.com/azuki774/kinakomate/internal/log"
)

// recordingDep records every call so tests can assert on ordering and which
// steps ran. It implements all four runner interfaces.
type recordingDep struct {
	calls         []string
	failOn        map[string]error
	scaleErrors   map[string][]error
	scaleReplicas []int
	waitTimeouts  []time.Duration
	apiTimeout    time.Duration
	lastDump      *Dump
	dumpPath      string
	dumpCleanup   func() error
}

func (d *recordingDep) CheckConnection(_ context.Context, _ *config.Config) error {
	d.calls = append(d.calls, "check")
	return d.failOn["check"]
}

type dbRecordingDep struct{ *recordingDep }

func (d *dbRecordingDep) CheckConnection(_ context.Context, _ *config.Config) error {
	d.calls = append(d.calls, "db-check")
	return d.failOn["db-check"]
}

type s3RecordingDep struct{ *recordingDep }

func (d *s3RecordingDep) CheckConnection(_ context.Context, _ *config.Config) error {
	d.calls = append(d.calls, "s3-check")
	return d.failOn["s3-check"]
}

type k8sRecordingDep struct{ *recordingDep }

func (d *k8sRecordingDep) CheckConnection(_ context.Context, _ *config.Config) error {
	d.calls = append(d.calls, "k8s-check")
	return d.failOn["k8s-check"]
}

func (d *recordingDep) DownloadAndExtract(_ context.Context, _ *config.Config) (*Dump, error) {
	d.calls = append(d.calls, "s3-download")
	d.lastDump = d.dump()
	return d.lastDump, d.failOn["s3-download"]
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
	key := "scale:" + workload + ":" + itoa(replicas)
	d.calls = append(d.calls, key)
	if errors := d.scaleErrors[key]; len(errors) > 0 {
		err := errors[0]
		d.scaleErrors[key] = errors[1:]
		return err
	}
	return d.failOn[key]
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
	path := d.dumpPath
	if path == "" {
		f, err := os.CreateTemp("", "kinakomate-dump-*.sql.gz")
		if err != nil {
			return &Dump{Path: ""}
		}
		f.Close() //nolint:errcheck
		path = f.Name()
	}
	return &Dump{Path: path, Bucket: "backups", Key: "daily.sql.gz", ETag: "\"etag\"", Size: 42, cleanupFn: d.dumpCleanup}
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

func newTestRunner(dep *recordingDep) *runner {
	return &runner{
		db:     &dbRecordingDep{recordingDep: dep},
		s3:     &s3RecordingDep{recordingDep: dep},
		k8s:    &k8sRecordingDep{recordingDep: dep},
		api:    dep,
		logger: log.NewWithWriter(io.Discard),
	}
}

func TestRunnerRunUsesSuppliedLogger(t *testing.T) {
	var output bytes.Buffer
	dep := &recordingDep{}
	r := &runner{
		db:     dep,
		s3:     dep,
		k8s:    dep,
		api:    dep,
		logger: log.NewWithWriter(&output),
	}

	if err := r.run(context.Background(), testConfig()); err != nil {
		t.Fatalf("run returned unexpected error: %v", err)
	}
	if !strings.Contains(output.String(), `"msg":"restore-test step start"`) {
		t.Fatalf("supplied logger did not receive runner logs: %q", output.String())
	}
}

func TestRunnerRun_Order(t *testing.T) {
	dep := &recordingDep{}
	r := newTestRunner(dep)

	if err := r.run(context.Background(), testConfig()); err != nil {
		t.Fatalf("run returned unexpected error: %v", err)
	}

	want := []string{
		"getreplicas:misskey-web",
		"getreplicas:misskey-db-v18",
		"s3-check",
		"k8s-check",
		"s3-download",
		"scale:misskey-web:0",
		"scale:misskey-db-v18:1",
		"wait:misskey-web:0",
		"wait:misskey-db-v18:1",
		"db-check",
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
	r := newTestRunner(dep)

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

func TestRunnerRun_StopsOnWebReadinessFailure(t *testing.T) {
	dep := &recordingDep{failOn: map[string]error{
		"wait:misskey-web:0": errors.New("web not ready"),
	}}
	r := newTestRunner(dep)

	err := r.run(context.Background(), testConfig())
	if err == nil {
		t.Fatal("expected run to fail when web readiness wait fails")
	}
	if !strings.Contains(err.Error(), "wait web replicas 0") {
		t.Fatalf("error = %v, want it to mention web readiness", err)
	}
	for _, c := range dep.calls {
		if c == "db-reset" || c == "db-restore" {
			t.Fatalf("calls = %v, database operations must not run before web readiness", dep.calls)
		}
	}
}

func TestRunnerRun_StopsOnDBReadinessFailure(t *testing.T) {
	dep := &recordingDep{failOn: map[string]error{
		"wait:misskey-db-v18:1": errors.New("db not ready"),
	}}
	r := newTestRunner(dep)

	err := r.run(context.Background(), testConfig())
	if err == nil {
		t.Fatal("expected run to fail when DB readiness wait fails")
	}
	if !strings.Contains(err.Error(), "wait db replicas 1") {
		t.Fatalf("error = %v, want it to mention DB readiness", err)
	}
	if got := countCalls(dep.calls, "db-check"); got != 0 {
		t.Fatalf("db-check calls = %d, want none before DB readiness failure (calls: %v)", got, dep.calls)
	}
	assertCallTail(t, dep.calls, []string{
		"scale:misskey-db-v18:1",
		"wait:misskey-web:0",
		"wait:misskey-db-v18:1",
		"scale:misskey-web:0",
	})
	for _, c := range dep.calls {
		if c == "db-reset" || c == "db-restore" {
			t.Fatalf("calls = %v, database operations must not run before DB readiness", dep.calls)
		}
	}
}

func TestRunnerRun_StopsOnRestoreFailure(t *testing.T) {
	dep := &recordingDep{failOn: map[string]error{"db-restore": errors.New("restore boom")}}
	r := newTestRunner(dep)

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
	r := newTestRunner(dep)

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
	r := newTestRunner(dep)

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
	r := newTestRunner(dep)

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

func TestRunnerRun_StopsOnS3ConnectionFailure(t *testing.T) {
	dep := &recordingDep{failOn: map[string]error{"s3-check": errors.New("connection boom")}}
	r := newTestRunner(dep)

	err := r.run(context.Background(), testConfig())
	if err == nil {
		t.Fatal("expected run to fail when a connection check fails")
	}
	if !strings.Contains(err.Error(), "s3 connection check") {
		t.Fatalf("error = %v, want it to mention S3 connection check", err)
	}
	assertCallTail(t, dep.calls, []string{"s3-check", "scale:misskey-web:0"})
	assertNoCalls(t, dep.calls, "s3-download")
}

func countCalls(calls []string, want string) int {
	count := 0
	for _, call := range calls {
		if call == want {
			count++
		}
	}
	return count
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

func TestRunnerRun_CleansStagedDumpAfterFailure(t *testing.T) {
	dep := &recordingDep{failOn: map[string]error{"db-restore": errors.New("restore boom")}}
	r := newTestRunner(dep)

	if err := r.run(context.Background(), testConfig()); err == nil {
		t.Fatal("expected run to fail when restore fails")
	}
	if dep.lastDump == nil {
		t.Fatal("download did not return a dump")
	}
	if _, err := os.Stat(dep.lastDump.Path); !os.IsNotExist(err) {
		t.Fatalf("staged dump still exists or stat failed: path=%q err=%v", dep.lastDump.Path, err)
	}
}
