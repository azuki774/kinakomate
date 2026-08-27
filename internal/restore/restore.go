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
// The remaining steps (dump fetch, restore, migration, readiness, checks) are
// implemented in later issues.
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

	r, err := buildRunner()
	if err != nil {
		return err
	}
	return r.run(ctx, cfg)
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
