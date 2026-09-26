package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/azuki774/kinakomate/internal/log"
	"github.com/azuki774/kinakomate/internal/notification"
	"github.com/azuki774/kinakomate/internal/restore"
)

const discordNotificationWebhookEnv = "DISCORD_NOTIFICATION_WEBHOOK"

// These variables keep the command boundary deterministic in tests without
// changing restore.Run's dependency graph.
var (
	runRestoreTest          = restore.RunWithSummary
	sendRestoreNotification = notification.SendRestoreResult
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
		summary, err := runRestoreTest(ctx, args[1:])
		if !errors.Is(err, flag.ErrHelp) {
			notifyRestoreTest(summary, err)
		}
		return err
	case "help", "--help", "-h":
		fmt.Println("usage: kinakomate <command> [flags]")
		fmt.Println("commands:")
		fmt.Println("  restore-test   run the database restore verification test")
		return nil
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func notifyRestoreTest(summary restore.RunSummary, restoreErr error) {
	webhookURL := strings.TrimSpace(os.Getenv(discordNotificationWebhookEnv))
	if webhookURL == "" {
		return
	}

	// Do not reuse the restore context: a canceled restore must still report
	// its failure. SendRestoreResult adds the bounded request timeout.
	result := notification.RestoreResult{
		Success:               restoreErr == nil,
		FailedPhase:           summary.FailedPhase,
		DatabaseSize:          summary.DatabaseSize,
		DatabaseSizeAvailable: summary.DatabaseSizeAvailable,
		DatabaseSizeAttempted: summary.DatabaseSizeAttempted,
		BackupSize:            summary.BackupSize,
		BackupSizeAvailable:   summary.BackupSizeAvailable,
		Total:                 summary.Total,
	}
	if summary.RecoveryAttempted {
		result.Recovery = &notification.Phase{Status: notificationStatus(summary.RecoveryStatus), Duration: summary.RecoveryDuration}
	}
	if summary.TempCleanupStatus != "" {
		result.TempCleanup = &notification.Phase{Status: notificationStatus(summary.TempCleanupStatus), Duration: summary.TempCleanupDuration}
	}
	for _, p := range summary.Phases {
		result.Phases = append(result.Phases, notification.Phase{Name: p.Name, Status: p.Status, Duration: p.Duration})
	}
	if err := sendRestoreNotification(context.Background(), webhookURL, result); err != nil {
		// Keep both the restore error and webhook URL out of notification logs.
		log.New().Warn("discord notification failed")
	}
}

func notificationStatus(status string) string {
	if status == "error" {
		return "failure"
	}
	return status
}
