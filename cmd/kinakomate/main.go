package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/azuki774/kinakomate/internal/backup"
	"github.com/azuki774/kinakomate/internal/log"
	"github.com/azuki774/kinakomate/internal/notification"
	"github.com/azuki774/kinakomate/internal/restore"
)

const discordNotificationWebhookEnv = "DISCORD_NOTIFICATION_WEBHOOK"

// These variables keep the command boundary deterministic in tests.
var (
	runBackup               = backup.RunWithSummary
	runRestoreTest          = restore.RunWithSummary
	sendBackupNotification  = notification.SendBackupResult
	sendRestoreNotification = notification.SendRestoreResult
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:])
	stop()

	if err != nil {
		logCommandError(os.Stderr, err)
		os.Exit(1)
	}
}

func logCommandError(w io.Writer, err error) {
	log.NewWithWriter(w).Error("kinakomate command failed", "error", err)
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kinakomate <command> [flags]\ncommands:\n  backup         create and verify a PostgreSQL backup\n  restore-test   run the database restore verification test")
	}

	switch args[0] {
	case "backup":
		summary, err := runBackup(ctx, args[1:])
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println("usage: kinakomate backup")
			fmt.Println("Create a PostgreSQL SQL gzip dump and upload it to S3.")
			fmt.Println("Required environment: DB_HOST DB_PORT DB_USER DB_PASS DB_NAME S3_REGION S3_BUCKET S3_KEY")
			fmt.Println("Optional environment: S3_ENDPOINT DISCORD_NOTIFICATION_WEBHOOK TMPDIR")
			fmt.Println("S3 authentication uses the AWS SDK credential chain.")
			return nil
		}
		notifyBackup(summary, err)
		return err
	case "restore-test":
		summary, err := runRestoreTest(ctx, args[1:])
		if !errors.Is(err, flag.ErrHelp) {
			notifyRestoreTest(summary, err)
		}
		return err
	case "help", "--help", "-h":
		fmt.Println("usage: kinakomate <command> [flags]")
		fmt.Println("commands:")
		fmt.Println("  backup         create and verify a PostgreSQL backup")
		fmt.Println("  restore-test   run the database restore verification test")
		return nil
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}
func notifyBackup(summary backup.RunSummary, backupErr error) {
	webhookURL := strings.TrimSpace(os.Getenv(discordNotificationWebhookEnv))
	if webhookURL == "" {
		return
	}

	result := notification.BackupResult{
		Success:             backupErr == nil,
		FailedPhase:         summary.FailedPhase,
		BackupSize:          summary.BackupSize,
		BackupSizeAvailable: summary.BackupSizeAvailable,
		UploadVerified:      summary.UploadVerified,
		DatabaseName:        summary.DatabaseName,
		S3Bucket:            summary.S3Bucket,
		S3Key:               summary.S3Key,
		CompletedAt:         summary.CompletedAt,
		Total:               summary.Total,
	}
	for _, p := range summary.Phases {
		result.Phases = append(result.Phases, notification.Phase{Name: p.Name, Status: p.Status, Duration: p.Duration})
	}
	if err := sendBackupNotification(context.Background(), webhookURL, result); err != nil {
		log.New().Warn("discord notification failed")
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
