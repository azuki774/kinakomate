package restore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDumpCleanupIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dump.sql.gz")
	if err := os.WriteFile(path, []byte("dump"), 0o600); err != nil {
		t.Fatalf("write dump: %v", err)
	}

	dump := &Dump{Path: path}
	dump.Cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("dump still exists or stat failed: %v", err)
	}
	dump.Cleanup()
}

func TestDumpCleanupReportsDirectRemoveCauseWithoutPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "staged-dump")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("mkdir staged dump: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), []byte("dump"), 0o600); err != nil {
		t.Fatalf("write staged dump child: %v", err)
	}

	err := (&Dump{Path: path}).cleanup()
	if err == nil {
		t.Fatal("cleanup succeeded for non-empty directory")
	}
	if !strings.Contains(err.Error(), "directory not empty") {
		t.Fatalf("cleanup error = %q, want directory cause", err)
	}
	if strings.Contains(err.Error(), path) {
		t.Fatalf("cleanup error exposed local path: %q", err)
	}
	if !errors.Is(err, syscall.ENOTEMPTY) {
		t.Fatalf("cleanup error = %v, want to unwrap ENOTEMPTY", err)
	}
}
