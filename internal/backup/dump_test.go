package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
)

const fakePgDumpModeEnv = "KINAKOMATE_FAKE_PG_DUMP_MODE"

func TestBuildPgDumpCommandQuotesDatabaseNameAndKeepsPasswordOutOfArgs(t *testing.T) {
	password := "  db-password-secret\t"
	database := "backup host=198.51.100.42 port=1'\\name"
	cfg := &config.BackupConfig{
		DBHost: "database.internal", DBPort: "5432", DBUser: "backup",
		DBPass: password, DBName: database,
	}
	t.Setenv("PGPASSWORD", "inherited-password-secret")

	cmd := buildPgDumpCommand(context.Background(), cfg)
	const wantDatabaseArg = "--dbname=dbname='backup host=198.51.100.42 port=1\\'\\\\name'"
	if cmd.Args[len(cmd.Args)-1] != wantDatabaseArg {
		t.Fatalf("database argument = %q, want %q", cmd.Args[len(cmd.Args)-1], wantDatabaseArg)
	}
	if strings.Contains(strings.Join(cmd.Args, "\x00"), password) || strings.Contains(strings.Join(cmd.Args, "\x00"), "inherited-password-secret") {
		t.Fatal("database password appeared in pg_dump arguments")
	}
	passwordEntries := 0
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "PGPASSWORD=") {
			passwordEntries++
			if entry != "PGPASSWORD="+password {
				t.Fatalf("PGPASSWORD entry = %q, want exact configured value", entry)
			}
		}
	}
	if passwordEntries != 1 {
		t.Fatalf("pg_dump environment has %d PGPASSWORD entries, want exactly one", passwordEntries)
	}
}

func TestDumpCompressesOutputAndDiscardsStderr(t *testing.T) {
	setFakePgDump(t, "success")
	var compressed bytes.Buffer
	if err := dump(context.Background(), &config.BackupConfig{DBPass: "db-password-secret"}, &compressed); err != nil {
		t.Fatalf("dump() error = %v", err)
	}
	reader, err := gzip.NewReader(&compressed)
	if err != nil {
		t.Fatalf("gzip.NewReader() error = %v", err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("reading gzip output: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("gzip reader close: %v", err)
	}
	if string(plain) != "SELECT dump-test;\n" {
		t.Fatalf("decompressed output = %q, want dump SQL", plain)
	}
}

func TestDumpFailureDoesNotExposeStderrOrPassword(t *testing.T) {
	setFakePgDump(t, "failure")
	var compressed bytes.Buffer
	err := dump(context.Background(), &config.BackupConfig{DBPass: "db-password-secret"}, &compressed)
	if err == nil {
		t.Fatal("dump() succeeded after pg_dump failed")
	}
	for _, sensitive := range []string{"stderr-secret", "db-password-secret", "SQL-secret"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Errorf("dump error leaked %q", sensitive)
		}
	}
}

func TestDumpWriterFailureStopsAndReapsProcess(t *testing.T) {
	setFakePgDump(t, "stream")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	err := dump(ctx, &config.BackupConfig{}, failingWriter{})
	if err == nil {
		t.Fatal("dump() succeeded with a failing output writer")
	}
	if time.Since(started) >= 4*time.Second {
		t.Fatal("dump process was not stopped promptly after the output writer failed")
	}
}

func setFakePgDump(t *testing.T, mode string) {
	t.Helper()
	t.Setenv(fakePgDumpModeEnv, mode)
	original := pgDumpCommand
	pgDumpCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFakePgDumpProcess$")
	}
	t.Cleanup(func() { pgDumpCommand = original })
}

func TestFakePgDumpProcess(t *testing.T) {
	switch os.Getenv(fakePgDumpModeEnv) {
	case "success":
		_, _ = io.WriteString(os.Stdout, "SELECT dump-test;\n")
		_, _ = io.WriteString(os.Stderr, "stderr-secret\n")
		os.Exit(0)
	case "failure":
		_, _ = io.WriteString(os.Stdout, "SQL-secret partial output\n")
		_, _ = io.WriteString(os.Stderr, "stderr-secret db-password-secret\n")
		os.Exit(23)
	case "stream":
		chunk := make([]byte, 64*1024)
		if _, err := rand.Read(chunk); err != nil {
			os.Exit(1)
		}
		for {
			if _, err := os.Stdout.Write(chunk); err != nil {
				os.Exit(0)
			}
		}
	default:
		return
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("disk-write-secret")
}
