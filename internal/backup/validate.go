package backup

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
)

func validateGzip(ctx context.Context, path string) (int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, false, errors.New("open compressed dump failed")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return 0, false, errors.New("stat compressed dump failed")
	}
	size := info.Size()

	reader, err := gzip.NewReader(file)
	if err != nil {
		closeErr := file.Close()
		if closeErr != nil {
			return size, true, errors.New("close compressed dump failed")
		}
		return size, true, errors.New("invalid gzip stream")
	}
	_, readErr := io.Copy(io.Discard, contextReader{ctx: ctx, reader: reader})
	gzipCloseErr := reader.Close()
	fileCloseErr := file.Close()
	if readErr != nil || gzipCloseErr != nil || fileCloseErr != nil {
		return size, true, errors.New("gzip stream validation failed")
	}
	return size, true, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
