package restore

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/azuki774/kinakomate/internal/config"
)

// capturePsql constructs a runPsql func that records every invocation (with
// the stdin bytes materialized) and returns the given error. The returned
// slice lets tests assert on connection target, command text, transaction
// flags, and call ordering without a real database.
func capturePsql(t *testing.T, wantErr error) (func(context.Context, *config.Config, psqlInvocation) (string, error), *[]psqlInvocation, *bool) {
	t.Helper()
	called := false
	invocations := []psqlInvocation{}
	fn := func(_ context.Context, _ *config.Config, inv psqlInvocation) (string, error) {
		called = true
		if inv.Stdin != nil {
			data, err := io.ReadAll(inv.Stdin)
			if err != nil {
				t.Errorf("reading psql stdin: %v", err)
			}
			inv.Stdin = bytes.NewReader(data)
		}
		invocations = append(invocations, inv)
		if wantErr != nil {
			return "psql boom", wantErr
		}
		return "", nil
	}
	return fn, &invocations, &called
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
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: config.DBName}

	run, invocations, called := capturePsql(t, nil)
	db := &database{runPsql: run}

	if err := db.Restore(context.Background(), cfg, dump); err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	if !*called {
		t.Fatal("expected psql to be invoked")
	}
	inv := (*invocations)[0]
	if inv.DBName != config.DBName {
		t.Errorf("restore DBName = %q, want %q", inv.DBName, config.DBName)
	}
	if !inv.SingleTransaction {
		t.Error("restore must run in a single transaction")
	}
	if inv.Stdin == nil {
		t.Fatal("expected restore to stream SQL via stdin")
	}
	got, _ := io.ReadAll(inv.Stdin)
	if string(got) != sql {
		t.Errorf("psql received = %q, want %q", got, sql)
	}
}

func TestRestore_PropagatesPsqlError(t *testing.T) {
	dumpPath := writeGzip(t, t.TempDir(), "SELECT 1;")
	dump := &Dump{Path: dumpPath}
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: config.DBName}

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
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: config.DBName}

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
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: config.DBName}

	run, _, called := capturePsql(t, nil)
	db := &database{runPsql: run}

	if err := db.Restore(context.Background(), cfg, dump); err == nil {
		t.Fatal("expected Restore to fail for an invalid gzip stream")
	}
	if *called {
		t.Fatal("psql must not be invoked when the gzip stream is invalid")
	}
}

// TestReset_RecreatesTargetDatabase verifies Reset terminates stray backends,
// drops the target database, and recreates it from template0 — in that order,
// against the maintenance database, outside any transaction.
func TestReset_RecreatesTargetDatabase(t *testing.T) {
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: config.DBName}

	run, invocations, _ := capturePsql(t, nil)
	db := &database{runPsql: run}

	if err := db.Reset(context.Background(), cfg); err != nil {
		t.Fatalf("Reset returned error: %v", err)
	}

	if len(*invocations) != 3 {
		t.Fatalf("invocations = %d, want 3: %+v", len(*invocations), *invocations)
	}

	wantFragments := []string{
		"pg_terminate_backend(pid)",
		"datname = 'misskey'",
		"DROP DATABASE IF EXISTS misskey;",
		"CREATE DATABASE misskey TEMPLATE template0;",
	}
	for i, inv := range *invocations {
		if inv.DBName != "postgres" {
			t.Errorf("invocation %d DBName = %q, want the maintenance database %q", i, inv.DBName, "postgres")
		}
		if inv.SingleTransaction {
			t.Errorf("invocation %d must not use --single-transaction (database DDL cannot run in a transaction)", i)
		}
		if inv.Stdin != nil {
			t.Errorf("invocation %d must run its SQL via --command, not stdin", i)
		}
	}
	for _, want := range wantFragments {
		found := false
		for _, inv := range *invocations {
			if strings.Contains(inv.Command, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no psql command contains %q; commands = %+v", want, *invocations)
		}
	}
	// Order: terminate must come before DROP, and DROP before CREATE.
	if idx := strings.Index((*invocations)[1].Command, "DROP DATABASE"); idx == -1 {
		t.Errorf("invocation 1 = %q, want DROP DATABASE", (*invocations)[1].Command)
	}
	if idx := strings.Index((*invocations)[2].Command, "CREATE DATABASE"); idx == -1 {
		t.Errorf("invocation 2 = %q, want CREATE DATABASE", (*invocations)[2].Command)
	}
}

// TestReset_StopsOnFirstFailure verifies Reset aborts at the first failing
// statement instead of continuing to drop or create the database.
func TestReset_StopsOnFirstFailure(t *testing.T) {
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: config.DBName}

	run, invocations, _ := capturePsql(t, errors.New("connection refused"))
	db := &database{runPsql: run}

	if err := db.Reset(context.Background(), cfg); err == nil {
		t.Fatal("expected Reset to fail when psql fails")
	}
	if len(*invocations) != 1 {
		t.Errorf("invocations = %d, want 1 (stop on first failure): %+v", len(*invocations), *invocations)
	}
}

// TestCheckConnection_TargetsMaintenanceDatabase verifies the connection check
// connects to the maintenance database so it also passes when the target
// database is missing (e.g. after a previous failed run).
func TestCheckConnection_TargetsMaintenanceDatabase(t *testing.T) {
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: config.DBName}

	run, invocations, called := capturePsql(t, nil)
	db := &database{runPsql: run}

	if err := db.CheckConnection(context.Background(), cfg); err != nil {
		t.Fatalf("CheckConnection returned error: %v", err)
	}
	if !*called {
		t.Fatal("expected psql to be invoked")
	}
	inv := (*invocations)[0]
	if inv.DBName != "postgres" {
		t.Errorf("check DBName = %q, want the maintenance database %q", inv.DBName, "postgres")
	}
	if inv.Command != "SELECT 1" {
		t.Errorf("check Command = %q, want SELECT 1", inv.Command)
	}
	if inv.SingleTransaction {
		t.Error("connection check must not use --single-transaction")
	}
}

// TestDefaultPsqlRestoreCmd verifies the real restore invocation uses
// ON_ERROR_STOP, a single transaction, streams SQL via --file=-, connects with
// the right host/port/user/db, and passes the password via the environment
// (never the argument list).
func TestDefaultPsqlRestoreCmd(t *testing.T) {
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: config.DBName}
	inv := psqlInvocation{DBName: cfg.DBName, Stdin: strings.NewReader("SELECT 1;"), SingleTransaction: true}

	cmd := buildPsqlCmd(context.Background(), cfg, inv)

	args := cmd.Args
	if !hasArg(args, "--set", "ON_ERROR_STOP=1") {
		t.Errorf("args = %v, want ON_ERROR_STOP=1", args)
	}
	if !hasFlag(args, "--single-transaction") {
		t.Errorf("args = %v, want --single-transaction", args)
	}
	if !hasArg(args, "--file", "-") {
		t.Errorf("args = %v, want --file -", args)
	}
	if !hasArg(args, "--dbname", cfg.DBName) {
		t.Errorf("args = %v, want --dbname %q", args, cfg.DBName)
	}
	for _, val := range []string{"db", "5432", "misskey"} {
		if !slices.Contains(args, val) {
			t.Errorf("args = %v, want to contain %q", args, val)
		}
	}
	for _, a := range args {
		if strings.Contains(a, "secret") {
			t.Errorf("password leaked into psql args: %v", args)
		}
	}

	// Password must be provided via PGPASSWORD in the environment.
	env := strings.Join(cmd.Env, "\n")
	if !strings.Contains(env, "PGPASSWORD=secret") {
		t.Errorf("env = %v, want PGPASSWORD=secret", cmd.Env)
	}
}

// TestDefaultPsqlAdminCmd verifies an admin invocation (database DDL) runs its
// command text via --command and never adds --single-transaction.
func TestDefaultPsqlAdminCmd(t *testing.T) {
	cfg := &config.Config{DBHost: "db", DBPort: "5432", DBUser: "misskey", DBPass: "secret", DBName: config.DBName}
	inv := psqlInvocation{DBName: "postgres", Command: "DROP DATABASE misskey;"}

	cmd := buildPsqlCmd(context.Background(), cfg, inv)

	args := cmd.Args
	if !hasArg(args, "--command", "DROP DATABASE misskey;") {
		t.Errorf("args = %v, want --command with the admin SQL", args)
	}
	if hasFlag(args, "--single-transaction") {
		t.Errorf("args = %v, must not contain --single-transaction", args)
	}
	if hasFlag(args, "--file") {
		t.Errorf("args = %v, must not contain --file", args)
	}
	if !hasArg(args, "--dbname", "postgres") {
		t.Errorf("args = %v, want --dbname postgres", args)
	}
}

// hasArg reports whether args contains the flag followed by the value.
func hasArg(args []string, name, value string) bool {
	for i, a := range args {
		if a == name && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}

// hasFlag reports whether args contains the standalone flag.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}
