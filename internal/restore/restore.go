package restore

import (
	"context"
	"flag"
	"fmt"

	"github.com/azuki774/kinakomate/internal/config"
	"github.com/azuki774/kinakomate/internal/log"
)

// Run executes the `restore-test` subcommand.
//
// Step 1 (pre-flight validation) is implemented here: it loads and validates
// the input contract from the environment and fails immediately (non-zero exit)
// when the input is invalid. This runs before any workload, database, or
// external resource is touched, satisfying "validation before scale".
//
// The runner then fetches the dump, restores it into a fresh database, starts
// the web workload, waits for the Misskey API, validates the global timeline,
// and cleans up the workloads.
func Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("restore-test", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := log.New()

	cfg, err := config.LoadFromEnv()
	if err != nil {
		return fmt.Errorf("pre-flight validation failed: %w", err)
	}

	logger.InfoContext(ctx, "pre-flight validation passed", configToArgs(cfg.Loggable())...)

	r, err := runnerFactory(ctx, cfg)
	if err != nil {
		return fmt.Errorf("initialize runner: %w", err)
	}
	return r.run(ctx, cfg)
}

// runnerFactory builds a runner with the real dependencies. It is a variable so
// tests can substitute a runner backed by fakes and keep the run hermetic.
var runnerFactory = func(ctx context.Context, cfg *config.Config) (*runner, error) {
	return newRunner(ctx, cfg)
}

// configToArgs flattens a map into alternating key/value arguments for
// slog's key-value logging style.
func configToArgs(m map[string]any) []any {
	args := make([]any, 0, len(m)*2)
	for k, v := range m {
		args = append(args, k, v)
	}
	return args
}
