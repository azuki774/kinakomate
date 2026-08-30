package restore

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azuki774/kinakomate/internal/config"
)

// capturePsql constructs a runPsql func that records whether it was called and
// returns the given error. It reads all of stdin so the test can assert on what
// psql received.
func capturePsql(t *testing.T, wantErr error) (func(context.Context, *config.Config, io.Reader) (string, error), *bytes.Buffer, *bool) {
	t.Helper()
	called := false
	var received bytes.Buffer
	fn := func(_ context.Context, _ *config.Config, stdin io.Reader) (string, error) {
		called = true
		if stdin != nil {
			if _, err := io.Copy(&received, stdin); err != nil {
				t.Errorf("reading psql stdin: %v", err)
			}
		}
		if wantErr != nil {
			return "psql boom", wantErr
		}
		return "", nil
	}
	return fn, &received, &called
}

func writeGzip(t *testing.T, dir, data string) string {
	t.Helper()
	path := filepath.Join(dir, "dump.sql.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create dump file: %v", err)
	}
	zw := gzip.NewWriter(f)
	if _, err := io.WriteString(zw, data); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close dump file: %v", err)
	}
	return path
}

func TestRestore_StreamsDecompressedSQLToPsql(t *testing.T) {
	sql := "CREATE TABLE t(id int);\nINSERT INTO t VALUES (1);\n"
	dumpPath := writeGzip(t, t.TempDir(), sql)
	dump := &Dump{Path: dumpPath, Bucket: "b", Key: "k", ETag: "e", Size: 3}
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: "misskey"}

	run, received, called := capturePsql(t, nil)
	db := &database{runPsql: run}

	if err := db.Restore(context.Background(), cfg, dump); err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	if !*called {
		t.Fatal("expected psql to be invoked")
	}
	if got := received.String(); got != sql {
		t.Errorf("psql received = %q, want %q", got, sql)
	}
}

func TestRestore_PropagatesPsqlError(t *testing.T) {
	dumpPath := writeGzip(t, t.TempDir(), "SELECT 1;")
	dump := &Dump{Path: dumpPath}
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: "misskey"}

	run, _, called := capturePsql(t, io.ErrClosedPipe)
	db := &database{runPsql: run}

	if err := db.Restore(context.Background(), cfg, dump); err == nil {
		t.Fatal("expected Restore to fail when psql fails")
	}
	if !*called {
		t.Fatal("expected psql to be invoked")
	}
}

func TestRestore_MissingDumpFile(t *testing.T) {
	dump := &Dump{Path: "/no/such/file.sql.gz"}
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: "misskey"}

	run, _, _ := capturePsql(t, nil)
	db := &database{runPsql: run}

	if err := db.Restore(context.Background(), cfg, dump); err == nil {
		t.Fatal("expected Restore to fail for a missing dump file")
	}
}

func TestRestore_InvalidGzip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.sql.gz")
	if err := os.WriteFile(path, []byte("not gzip"), 0o600); err != nil {
		t.Fatalf("write bad dump: %v", err)
	}
	dump := &Dump{Path: path}
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: "misskey"}

	run, _, called := capturePsql(t, nil)
	db := &database{runPsql: run}

	if err := db.Restore(context.Background(), cfg, dump); err == nil {
		t.Fatal("expected Restore to fail for an invalid gzip stream")
	}
	if *called {
		t.Fatal("psql must not be invoked when the gzip stream is invalid")
	}
}

// TestDefaultPsqlArgs verifies the real psql invocation uses ON_ERROR_STOP, a
// single transaction, connects with the right host/port/user/db, and passes the
// password via the environment (never the argument list).
func TestDefaultPsqlArgs(t *testing.T) {
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: "misskey"}

	cmd := buildPsqlCmd(context.Background(), cfg, "SELECT 1", nil)

	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "ON_ERROR_STOP=1") {
		t.Errorf("args = %v, want ON_ERROR_STOP=1", cmd.Args)
	}
	if !strings.Contains(args, "--single-transaction") {
		t.Errorf("args = %v, want --single-transaction", cmd.Args)
	}
	for _, val := range []string{"db", "5432", "misskey"} {
		if !strings.Contains(args, val) {
			t.Errorf("args = %v, want to contain %q", cmd.Args, val)
		}
	}
	for _, a := range cmd.Args {
		if strings.Contains(a, "secret") {
			t.Errorf("password leaked into psql args: %v", cmd.Args)
		}
	}

	// Password must be provided via PGPASSWORD in the environment.
	env := strings.Join(cmd.Env, "\n")
	if !strings.Contains(env, "PGPASSWORD=secret") {
		t.Errorf("env = %v, want PGPASSWORD=secret", cmd.Env)
	}
}

// TestDefaultPsqlCmdCommandText asserts a nil stdin adds --command.
func TestDefaultPsqlCmdCommandText(t *testing.T) {
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: "misskey"}

	cmd := buildPsqlCmd(context.Background(), cfg, "SELECT 1", nil)
	joined := strings.Join(cmd.Args, " ")
	if !strings.Contains(joined, "--command SELECT 1") {
		t.Errorf("args = %v, want --command SELECT 1", cmd.Args)
	}
}
