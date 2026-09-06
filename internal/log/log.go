package log

import (
	"io"
	"log/slog"
	"os"
)

// New returns a structured logger that writes JSON to stderr.
func New() *slog.Logger {
	return NewWithWriter(os.Stderr)
}

// NewWithWriter returns a structured logger that writes JSON to w.
func NewWithWriter(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}
