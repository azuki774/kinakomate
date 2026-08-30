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
}

func (d *recordingDep) CheckConnection(_ context.Context, _ *config.Config) error {
	d.calls = append(d.calls, "check")
	return d.failOn["check"]
}

func (d *recordingDep) DownloadAndExtract(_ context.Context, _ *config.Config) (*Dump, error) {
	d.calls = append(d.calls, "s3-download")
	return d.dump(), d.failOn["s3-download"]
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

func (d *recordingDep) WaitForReplicas(_ context.Context, _ *config.Config, workload string, want int, _ time.Duration) error {
	d.calls = append(d.calls, "wait:"+workload+":"+itoa(want))
	return d.failOn["wait:"+workload+":"+itoa(want)]
}

func (d *recordingDep) Run(_ context.Context, _ *config.Config) error {
	d.calls = append(d.calls, "checks")
	return d.failOn["checks"]
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
	return &config.Config{WebWorkload: "misskey-web", DBWorkload: "misskey-db-v18", DBName: config.DBName}
}

func TestRunnerRun_Order(t *testing.T) {
	dep := &recordingDep{}
	r := &runner{db: dep, s3: dep, k8s: dep, chk: dep}

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
		"db-restore",
		"scale:misskey-web:1",
		"checks",
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
}

func TestRunnerRun_StopsOnRestoreFailure(t *testing.T) {
	dep := &recordingDep{failOn: map[string]error{"db-restore": errors.New("restore boom")}}
	r := &runner{db: dep, s3: dep, k8s: dep, chk: dep}

	err := r.run(context.Background(), testConfig())
	if err == nil {
		t.Fatal("expected run to fail when restore fails")
	}
	if !strings.Contains(err.Error(), "db restore") {
		t.Fatalf("error = %v, want it to mention db restore", err)
	}

	// After a restore failure the web must be rolled back to 0 (via deferred
	// rollback) but scale-to-1, checks, and cleanup must never run.
	for _, c := range dep.calls {
		if c == "scale:misskey-web:1" || c == "checks" || c == "scale:misskey-db-v18:0" {
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
