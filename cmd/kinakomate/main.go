package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/azuki774/kinakomate/internal/log"
	"github.com/azuki774/kinakomate/internal/restore"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		logCommandError(os.Stderr, err)
		os.Exit(1)
	}
}

func logCommandError(w io.Writer, err error) {
	log.NewWithWriter(w).Error("kinakomate command failed", "error", err)
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kinakomate <command> [flags]\ncommands:\n  restore-test   run the database restore verification test")
	}

	switch args[0] {
	case "restore-test":
		return restore.Run(ctx, args[1:])
	case "help", "--help", "-h":
		fmt.Println("usage: kinakomate <command> [flags]")
		fmt.Println("commands:")
		fmt.Println("  restore-test   run the database restore verification test")
		return nil
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}
