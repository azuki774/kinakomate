package restore

import (
	"context"
	"errors"
	"strings"
	"testing"

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

func (d *recordingDep) DownloadAndExtract(_ context.Context, _ *config.Config) error {
	d.calls = append(d.calls, "s3-download")
	return d.failOn["s3-download"]
}

func (d *recordingDep) Restore(_ context.Context, _ *config.Config) error {
	d.calls = append(d.calls, "db-restore")
	return d.failOn["db-restore"]
}

func (d *recordingDep) Scale(_ context.Context, _ *config.Config, replicas int) error {
	d.scaleReplicas = append(d.scaleReplicas, replicas)
	d.calls = append(d.calls, "scale:"+itoa(replicas))
	return d.failOn["scale:"+itoa(replicas)]
}

func (d *recordingDep) Run(_ context.Context, _ *config.Config) error {
	d.calls = append(d.calls, "checks")
	return d.failOn["checks"]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return "1"
}

func testConfig() *config.Config {
	return &config.Config{Workload: "misskey", DBName: config.DBName}
}

func TestRunnerRun_Order(t *testing.T) {
	dep := &recordingDep{}
	r := &runner{db: dep, s3: dep, k8s: dep, chk: dep}

	if err := r.run(context.Background(), testConfig()); err != nil {
		t.Fatalf("run returned unexpected error: %v", err)
	}

	want := []string{
		"check",
		"check",
		"check",
		"s3-download",
		"scale:0",
		"db-restore",
		"scale:1",
		"checks",
	}
	if len(dep.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", dep.calls, want)
	}
	for i := range want {
		if dep.calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q (full: %v)", i, dep.calls[i], want[i], dep.calls)
		}
	}

	if len(dep.scaleReplicas) != 2 || dep.scaleReplicas[0] != 0 || dep.scaleReplicas[1] != 1 {
		t.Fatalf("scaleReplicas = %v, want [0 1]", dep.scaleReplicas)
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

	// After a restore failure the workload must stay at 0: scale-to-1 and
	// checks must never run.
	for _, c := range dep.calls {
		if c == "scale:1" || c == "checks" {
			t.Fatalf("calls = %v, scale:1/checks must not run after restore failure", dep.calls)
		}
	}
	if len(dep.scaleReplicas) != 1 || dep.scaleReplicas[0] != 0 {
		t.Fatalf("scaleReplicas = %v, want [0]", dep.scaleReplicas)
	}
}
