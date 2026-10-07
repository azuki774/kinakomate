package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateGzipRejectsTruncatedArchiveAndReportsSize(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte("SELECT 1;\n")); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	truncated := compressed.Bytes()[:compressed.Len()-4]
	path := filepath.Join(t.TempDir(), "truncated.sql.gz")
	if err := os.WriteFile(path, truncated, 0o600); err != nil {
		t.Fatalf("write truncated archive: %v", err)
	}

	size, available, err := validateGzip(context.Background(), path)
	if err == nil {
		t.Fatal("validateGzip() accepted a truncated archive")
	}
	if !available || size != int64(len(truncated)) {
		t.Fatalf("validateGzip() size = (%d, %t), want (%d, true)", size, available, len(truncated))
	}
}

func TestValidateGzipRejectsCorruptChecksum(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte("SELECT 1;\n")); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	corrupt := append([]byte(nil), compressed.Bytes()...)
	corrupt[len(corrupt)-8] ^= 0xff
	path := filepath.Join(t.TempDir(), "corrupt.sql.gz")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatalf("write corrupt archive: %v", err)
	}

	if _, _, err := validateGzip(context.Background(), path); err == nil {
		t.Fatal("validateGzip() accepted a corrupt checksum")
	}
}
