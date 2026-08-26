package restore

import (
	"context"
	"flag"
	"fmt"

	"github.com/azuki774/kinakomate/internal/log"
)

// Run executes the `restore-test` subcommand.
//
// It currently validates its input and reports what it would do. The actual
// restore verification flow (dump fetch, restore, migration, readiness, checks)
// is implemented in later issues.
func Run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("restore-test", flag.ContinueOnError)
	configPath := fs.String("config", "", "path to the config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	logger := log.New()
	logger.InfoContext(ctx, "restore-test invoked", "config", *configPath)

	fmt.Println("restore-test: not yet implemented")

	return nil
}
