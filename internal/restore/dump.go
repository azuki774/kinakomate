package restore

import (
	"errors"
	"os"
)

// Dump describes a fixed backup object fetched from object storage and staged
// on disk as a gzip file. It carries the object metadata (bucket, key, ETag,
// size) for structured logging and the path to the staged gzip file that the
// restore step streams into psql.
//
// The gzip stream is never expanded to a plaintext SQL file on disk, so the
// caller must remove the file when done (see Cleanup).
type Dump struct {
	Path   string // absolute path to the staged gzip dump
	Bucket string
	Key    string
	ETag   string
	Size   int64

	cleanupFn func() error
}

// Cleanup removes the staged gzip file, retaining the original void API for
// callers that register it as a test cleanup function.
func (d *Dump) Cleanup() {
	_ = d.cleanup()
}

func (d *Dump) cleanup() error {
	if d == nil || d.Path == "" {
		return nil
	}
	if d.cleanupFn != nil {
		err := d.cleanupFn()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(d.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return &dumpCleanupError{err: err}
	}
	return nil
}

type dumpCleanupError struct {
	err error
}

func (e *dumpCleanupError) Error() string {
	cause := e.err
	for {
		unwrapped := errors.Unwrap(cause)
		if unwrapped == nil {
			break
		}
		cause = unwrapped
	}
	return "remove staged dump failed: " + cause.Error()
}

func (e *dumpCleanupError) Unwrap() error {
	return e.err
}
