package log

import (
	"log/slog"
	"os"
)

// New returns a structured logger that writes JSON to stderr.
func New() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}
