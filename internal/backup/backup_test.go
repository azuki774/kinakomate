package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
	"github.com/azuki774/kinakomate/internal/log"
)

type recordingStore struct {
	uploadErr     error
	headErr       error
	headSize      *int64
	uploadCalled  bool
	headCalled    bool
	uploadedBytes int64
}

func (s *recordingStore) Upload(_ context.Context, _, _ string, body io.ReadSeeker) error {
	s.uploadCalled = true
	if s.uploadErr != nil {
		return s.uploadErr
	}
	size, err := io.Copy(io.Discard, body)
	s.uploadedBytes = size
	return err
}

func (s *recordingStore) HeadObjectSize(_ context.Context, _, _ string) (int64, error) {
	s.headCalled = true
	if s.headErr != nil {
		return 0, s.headErr
	}
	if s.headSize != nil {
		return *s.headSize, nil
	}
	return s.uploadedBytes, nil
}

func testBackupConfig() *config.BackupConfig {
	return &config.BackupConfig{
		DBHost: "db", DBPort: "5432", DBUser: "backup", DBPass: "db-pass-secret",
		DBName: "app", S3Region: "us-east-1", S3Bucket: "backup-bucket",
		S3Key: "backup-key-secret.sql.gz",
	}
}

func testDependencies(t *testing.T, store objectStore) backupDependencies {
	t.Helper()
	deps := defaultDependencies()
	deps.loadConfig = func() (*config.BackupConfig, error) {
		return testBackupConfig(), nil
	}
	deps.newStore = func(context.Context, *config.BackupConfig) (objectStore, error) {
		return store, nil
	}
	deps.createTemp = func() (dumpFile, error) {
		return os.CreateTemp(t.TempDir(), "backup-test-*.sql.gz")
	}
	deps.dump = func(_ context.Context, _ *config.BackupConfig, dst io.Writer) error {
		writer := gzip.NewWriter(dst)
		if _, err := io.WriteString(writer, "SELECT backup-test;\n"); err != nil {
			return err
		}
		return writer.Close()
	}
	return deps
}

func TestRunWithDependenciesHelpDoesNotLoadConfigOrInitializeStorage(t *testing.T) {
	loaded := false
	initialized := false
	deps := defaultDependencies()
	deps.loadConfig = func() (*config.BackupConfig, error) {
		loaded = true
		return nil, nil
	}
	deps.newStore = func(context.Context, *config.BackupConfig) (objectStore, error) {
		initialized = true
		return nil, nil
	}
	var output bytes.Buffer

	summary, err := runWithDependencies(context.Background(), []string{"--help"}, deps, log.NewWithWriter(&output))
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("runWithDependencies() error = %v, want flag.ErrHelp", err)
	}
	if loaded || initialized {
		t.Fatalf("help performed initialization: load=%t storage=%t", loaded, initialized)
	}
	if output.Len() != 0 {
		t.Fatalf("help emitted a backup report: %q", output.String())
	}
	for _, phase := range summary.Phases {
		if phase.Status != "skipped" {
			t.Errorf("help phase %q status = %q, want skipped", phase.Name, phase.Status)
		}
	}
}

func TestRunWithDependenciesCanceledBeforePreflightDoesNoInitialization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loaded := false
	initialized := false
	deps := defaultDependencies()
	deps.loadConfig = func() (*config.BackupConfig, error) {
		loaded = true
		return testBackupConfig(), nil
	}
	deps.newStore = func(context.Context, *config.BackupConfig) (objectStore, error) {
		initialized = true
		return &recordingStore{}, nil
	}

	summary, err := runWithDependencies(ctx, nil, deps, log.NewWithWriter(io.Discard))
	if err == nil || summary.FailedPhase != "preflight" {
		t.Fatalf("run result = (%+v, %v), want preflight failure", summary, err)
	}
	if loaded || initialized {
		t.Fatalf("canceled preflight performed initialization: load=%t storage=%t", loaded, initialized)
	}
}

func TestRunWithDependenciesDumpFailureSkipsUploadAndCleansTemp(t *testing.T) {
	store := &recordingStore{}
	deps := testDependencies(t, store)
	var tempPath string
	createTemp := deps.createTemp
	deps.createTemp = func() (dumpFile, error) {
		file, err := createTemp()
		if err == nil {
			tempPath = file.Name()
		}
		return file, err
	}
	deps.dump = func(context.Context, *config.BackupConfig, io.Writer) error {
		return errors.New("stderr-secret SQL-secret")
	}
	var output bytes.Buffer

	summary, err := runWithDependencies(context.Background(), nil, deps, log.NewWithWriter(&output))
	if err == nil {
		t.Fatalf("run error = %v, want generic dump error", err)
	}
	if summary.FailedPhase != "dump" || summary.Phases[2].Status != "skipped" || summary.Phases[3].Status != "skipped" {
		t.Fatalf("unexpected phases after dump failure: %+v", summary.Phases)
	}
	if store.uploadCalled {
		t.Fatal("upload was attempted after dump failure")
	}
	if summary.Phases[4].Status != "success" {
		t.Fatalf("cleanup status = %q, want success", summary.Phases[4].Status)
	}
	if _, statErr := os.Stat(tempPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temporary file remains after cleanup: %v", statErr)
	}
	for _, sensitive := range []string{"stderr-secret", "SQL-secret", "db-pass-secret", "backup-key-secret"} {
		if strings.Contains(err.Error()+output.String(), sensitive) {
			t.Errorf("failure output leaked %q", sensitive)
		}
	}
}

func TestRunWithDependenciesInvalidGzipSkipsUpload(t *testing.T) {
	store := &recordingStore{}
	deps := testDependencies(t, store)
	deps.dump = func(_ context.Context, _ *config.BackupConfig, dst io.Writer) error {
		_, err := io.WriteString(dst, "not-gzip")
		return err
	}

	summary, err := runWithDependencies(context.Background(), nil, deps, log.NewWithWriter(io.Discard))
	if err == nil {
		t.Fatalf("run error = %v, want generic validation error", err)
	}
	if store.uploadCalled || store.headCalled {
		t.Fatal("S3 was accessed after gzip validation failed")
	}
	if summary.FailedPhase != "validate" || summary.Phases[3].Status != "skipped" || summary.Phases[4].Status != "success" {
		t.Fatalf("unexpected phases after validation failure: %+v", summary.Phases)
	}
	if !summary.BackupSizeAvailable || summary.BackupSize == 0 {
		t.Fatalf("compressed size was not recorded: %+v", summary)
	}
}

func TestRunWithDependenciesHeadMismatchFailsUpload(t *testing.T) {
	wrongSize := int64(1)
	store := &recordingStore{headSize: &wrongSize}
	deps := testDependencies(t, store)

	summary, err := runWithDependencies(context.Background(), nil, deps, log.NewWithWriter(io.Discard))
	if err == nil {
		t.Fatalf("run error = %v, want generic upload error", err)
	}
	if summary.UploadVerified || summary.FailedPhase != "upload" || store.uploadCalled != true || store.headCalled != true {
		t.Fatalf("unexpected upload state: summary=%+v store=%+v", summary, store)
	}
	if summary.Phases[4].Status != "success" {
		t.Fatalf("cleanup status = %q, want success", summary.Phases[4].Status)
	}
}

func TestRunWithDependenciesCleanupFailurePreservesVerifiedUploadAndHidesPaths(t *testing.T) {
	store := &recordingStore{}
	deps := testDependencies(t, store)
	var tempPath string
	createTemp := deps.createTemp
	deps.createTemp = func() (dumpFile, error) {
		file, err := createTemp()
		if err == nil {
			tempPath = file.Name()
		}
		return file, err
	}
	deps.remove = func(string) error { return errors.New("temporary-path-secret") }
	var output bytes.Buffer

	summary, err := runWithDependencies(context.Background(), nil, deps, log.NewWithWriter(&output))
	if err == nil {
		t.Fatalf("run error = %v, want cleanup failure", err)
	}
	if !summary.UploadVerified || !summary.BackupSizeAvailable || summary.FailedPhase != "cleanup" {
		t.Fatalf("verified upload state was lost: %+v", summary)
	}
	if summary.Phases[3].Status != "success" || summary.Phases[4].Status != "failure" {
		t.Fatalf("unexpected upload/cleanup phases: %+v", summary.Phases[3:])
	}
	for _, sensitive := range []string{"temporary-path-secret", tempPath, "db-pass-secret", "backup-key-secret"} {
		if sensitive != "" && strings.Contains(err.Error()+output.String(), sensitive) {
			t.Errorf("failure output leaked %q", sensitive)
		}
	}
	if err := os.Remove(tempPath); err != nil {
		t.Fatalf("remove test temporary file: %v", err)
	}
}

func TestRunWithDependenciesReturnsSafeConfigurationError(t *testing.T) {
	storeInitialized := false
	deps := defaultDependencies()
	deps.loadConfig = func() (*config.BackupConfig, error) {
		return nil, errors.New("required input DB_PASS is missing or empty")
	}
	deps.newStore = func(context.Context, *config.BackupConfig) (objectStore, error) {
		storeInitialized = true
		return &recordingStore{}, nil
	}

	summary, err := runWithDependencies(context.Background(), nil, deps, log.NewWithWriter(io.Discard))
	if err == nil {
		t.Fatalf("run error = %v, want safe config detail", err)
	}
	if summary.FailedPhase != "preflight" || storeInitialized {
		t.Fatalf("unexpected preflight result: summary=%+v initialized=%t", summary, storeInitialized)
	}
	if summary.DatabaseName != "" || summary.S3Bucket != "" || summary.S3Key != "" {
		t.Fatalf("identity recorded without a loaded configuration: %+v", summary)
	}
}

func TestRunWithDependenciesRetainsTargetIdentityAfterStorageInitializationFailure(t *testing.T) {
	deps := defaultDependencies()
	deps.loadConfig = func() (*config.BackupConfig, error) {
		return testBackupConfig(), nil
	}
	deps.newStore = func(context.Context, *config.BackupConfig) (objectStore, error) {
		return nil, errors.New("s3 endpoint secret")
	}

	summary, err := runWithDependencies(context.Background(), nil, deps, log.NewWithWriter(io.Discard))
	if err == nil || summary.FailedPhase != "preflight" {
		t.Fatalf("run result = (%+v, %v), want preflight failure", summary, err)
	}
	if summary.DatabaseName != "app" || summary.S3Bucket != "backup-bucket" || summary.S3Key != "backup-key-secret.sql.gz" {
		t.Fatalf("target identity lost after storage initialization failure: %+v", summary)
	}
}

func TestRunResultFinishDerivesTotalFromCompletionInstant(t *testing.T) {
	started := time.Now().Add(-time.Second)
	result := newRunResult(started)

	summary, err := result.finish()
	if err != nil {
		t.Fatalf("finish() error = %v, want nil", err)
	}
	if summary.CompletedAt.IsZero() {
		t.Fatal("finish() did not record a completion time")
	}
	if want := summary.CompletedAt.Sub(started); summary.Total != want {
		t.Fatalf("Total = %v, want %v derived from the same completion instant", summary.Total, want)
	}
}

func TestRunWithDependenciesFileCloseFailureSkipsUpload(t *testing.T) {
	store := &recordingStore{}
	deps := testDependencies(t, store)
	var tempPath string
	createTemp := deps.createTemp
	deps.createTemp = func() (dumpFile, error) {
		file, err := createTemp()
		if err != nil {
			return nil, err
		}
		tempPath = file.Name()
		return closeFailureDumpFile{File: file.(*os.File)}, nil
	}

	summary, err := runWithDependencies(context.Background(), nil, deps, log.NewWithWriter(io.Discard))
	if err == nil {
		t.Fatalf("run error = %v, want generic dump error", err)
	}
	if store.uploadCalled || summary.FailedPhase != "dump" || summary.Phases[4].Status != "success" {
		t.Fatalf("unexpected state after file close failure: summary=%+v store=%+v", summary, store)
	}
	if _, statErr := os.Stat(tempPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temporary file remains after cleanup: %v", statErr)
	}
}

func TestRunWithDependenciesUploadFailureSkipsHeadAndCleansTemp(t *testing.T) {
	store := &recordingStore{uploadErr: errors.New("S3-body-secret")}
	deps := testDependencies(t, store)
	var output bytes.Buffer

	summary, err := runWithDependencies(context.Background(), nil, deps, log.NewWithWriter(&output))
	if err == nil {
		t.Fatalf("run error = %v, want generic upload error", err)
	}
	if !store.uploadCalled || store.headCalled || summary.FailedPhase != "upload" || summary.Phases[4].Status != "success" {
		t.Fatalf("unexpected state after upload failure: summary=%+v store=%+v", summary, store)
	}
	if strings.Contains(err.Error()+output.String(), "S3-body-secret") {
		t.Fatal("upload error leaked SDK response detail")
	}
}

func TestRunWithDependenciesCleanupFailurePreservesEarlierFailure(t *testing.T) {
	store := &recordingStore{}
	deps := testDependencies(t, store)
	var tempPath string
	createTemp := deps.createTemp
	deps.createTemp = func() (dumpFile, error) {
		file, err := createTemp()
		if err == nil {
			tempPath = file.Name()
		}
		return file, err
	}
	deps.dump = func(context.Context, *config.BackupConfig, io.Writer) error {
		return errors.New("pg-dump-secret")
	}
	deps.remove = func(string) error { return errors.New("cleanup-path-secret") }

	summary, err := runWithDependencies(context.Background(), nil, deps, log.NewWithWriter(io.Discard))
	if err == nil || summary.FailedPhase != "dump" {
		t.Fatalf("run result = (%+v, %v), want original dump failure", summary, err)
	}
	if summary.Phases[4].Status != "failure" || store.uploadCalled {
		t.Fatalf("cleanup or upload state incorrect: summary=%+v store=%+v", summary, store)
	}
	if removeErr := os.Remove(tempPath); removeErr != nil {
		t.Fatalf("remove test temporary file: %v", removeErr)
	}
}

type closeFailureDumpFile struct {
	*os.File
}

func (f closeFailureDumpFile) Close() error {
	if err := f.File.Close(); err != nil {
		return err
	}
	return errors.New("temporary-file-close-secret")
}
