package restore

import (
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
}

// Cleanup removes the staged gzip file, ignoring any error.
func (d *Dump) Cleanup() {
	if d == nil || d.Path == "" {
		return
	}
	_ = os.Remove(d.Path)
}
