package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"testing"
	"time"

	"github.com/azuki774/kinakomate/internal/notification"
	"github.com/azuki774/kinakomate/internal/restore"
)

func TestRun_CommandErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing command", args: nil},
		{name: "unknown command", args: []string{"unknown"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := run(context.Background(), tt.args); err == nil {
				t.Fatal("run succeeded, want command error")
			}
		})
	}
}

func TestRunRestoreTestNotifiesSuccessAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		restoreErr error
		success    bool
	}{
		{name: "success", success: true},
		{name: "failure", restoreErr: errors.New("restore details"), success: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(discordNotificationWebhookEnv, "https://discord.example/webhook-secret")

			oldRun := runRestoreTest
			oldNotify := sendRestoreNotification
			t.Cleanup(func() {
				runRestoreTest = oldRun
				sendRestoreNotification = oldNotify
			})

			runRestoreTest = func(context.Context, []string) (restore.RunSummary, error) {
				return restore.RunSummary{DatabaseSize: 1234, DatabaseSizeAvailable: true, BackupSize: 567, BackupSizeAvailable: true, Total: time.Minute, Phases: []restore.PhaseSummary{{Name: "restore", Status: "success", Duration: time.Second}}}, tc.restoreErr
			}
			var gotURL string
			var gotSuccess bool
			sendRestoreNotification = func(ctx context.Context, webhookURL string, result notification.RestoreResult) error {
				if ctx.Err() != nil {
					t.Errorf("notification context is canceled: %v", ctx.Err())
				}
				gotURL = webhookURL
				gotSuccess = result.Success
				if result.DatabaseSize != 1234 || !result.DatabaseSizeAvailable || result.BackupSize != 567 || !result.BackupSizeAvailable || result.Total != time.Minute || len(result.Phases) != 1 || result.Phases[0].Duration != time.Second {
					t.Errorf("metrics not forwarded: %+v", result)
				}
				return nil
			}

			if err := run(context.Background(), []string{"restore-test"}); !errors.Is(err, tc.restoreErr) {
				t.Fatalf("run error = %v, want %v", err, tc.restoreErr)
			}
			if gotURL != "https://discord.example/webhook-secret" {
				t.Errorf("webhook URL = %q, want configured URL", gotURL)
			}
			if gotSuccess != tc.success {
				t.Errorf("success = %t, want %t", gotSuccess, tc.success)
			}
		})
	}
}

func TestRun_HelpSucceeds(t *testing.T) {
	if err := run(context.Background(), []string{"help"}); err != nil {
		t.Fatalf("run help returned error: %v", err)
	}
}

func TestLogCommandErrorWritesStructuredJSON(t *testing.T) {
	var output bytes.Buffer
	logCommandError(&output, errors.New("unknown command: nope"))

	var record map[string]any
	if err := json.Unmarshal(output.Bytes(), &record); err != nil {
		t.Fatalf("command error is not JSON: %v; output=%q", err, output.String())
	}
	if record["msg"] != "kinakomate command failed" {
		t.Fatalf("msg = %#v, want structured command message", record["msg"])
	}
	if record["error"] != "unknown command: nope" {
		t.Fatalf("error = %#v, want command error", record["error"])
	}
	if bytes.Contains(output.Bytes(), []byte("kinakomate: unknown")) {
		t.Fatalf("output contains legacy plain-text error: %q", output.String())
	}
}

func TestRunRestoreTestSkipsMissingWebhookAndHelp(t *testing.T) {
	t.Setenv(discordNotificationWebhookEnv, "")
	oldRun := runRestoreTest
	oldNotify := sendRestoreNotification
	t.Cleanup(func() {
		runRestoreTest = oldRun
		sendRestoreNotification = oldNotify
	})

	notifications := 0
	sendRestoreNotification = func(context.Context, string, notification.RestoreResult) error {
		notifications++
		return nil
	}
	runRestoreTest = func(context.Context, []string) (restore.RunSummary, error) { return restore.RunSummary{}, nil }
	if err := run(context.Background(), []string{"restore-test"}); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	t.Setenv(discordNotificationWebhookEnv, "https://discord.example/webhook")
	runRestoreTest = func(context.Context, []string) (restore.RunSummary, error) { return restore.RunSummary{}, flag.ErrHelp }
	if err := run(context.Background(), []string{"restore-test", "--help"}); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("run error = %v, want flag.ErrHelp", err)
	}
	if notifications != 0 {
		t.Fatalf("notifications = %d, want 0", notifications)
	}
}

func TestRunUnknownCommandDoesNotNotify(t *testing.T) {
	t.Setenv(discordNotificationWebhookEnv, "https://discord.example/webhook")
	oldNotify := sendRestoreNotification
	t.Cleanup(func() { sendRestoreNotification = oldNotify })

	notifications := 0
	sendRestoreNotification = func(context.Context, string, notification.RestoreResult) error {
		notifications++
		return nil
	}
	if err := run(context.Background(), []string{"unknown"}); err == nil {
		t.Fatal("expected unknown command error")
	}
	if notifications != 0 {
		t.Fatalf("notifications = %d, want 0", notifications)
	}
}

func TestRunNotificationFailurePreservesRestoreError(t *testing.T) {
	t.Setenv(discordNotificationWebhookEnv, "https://discord.example/webhook")
	oldRun := runRestoreTest
	oldNotify := sendRestoreNotification
	t.Cleanup(func() {
		runRestoreTest = oldRun
		sendRestoreNotification = oldNotify
	})

	restoreErr := errors.New("restore details")
	runRestoreTest = func(context.Context, []string) (restore.RunSummary, error) { return restore.RunSummary{}, restoreErr }
	sendRestoreNotification = func(context.Context, string, notification.RestoreResult) error {
		return errors.New("webhook transport details")
	}
	if err := run(context.Background(), []string{"restore-test"}); !errors.Is(err, restoreErr) {
		t.Fatalf("run error = %v, want original restore error", err)
	}
}

func TestRunNotificationUsesFreshContextAfterRestoreCancellation(t *testing.T) {
	t.Setenv(discordNotificationWebhookEnv, "https://discord.example/webhook")
	oldRun := runRestoreTest
	oldNotify := sendRestoreNotification
	t.Cleanup(func() {
		runRestoreTest = oldRun
		sendRestoreNotification = oldNotify
	})

	var restoreCanceled, notificationCanceled bool
	runRestoreTest = func(ctx context.Context, _ []string) (restore.RunSummary, error) {
		restoreCanceled = errors.Is(ctx.Err(), context.Canceled)
		return restore.RunSummary{}, context.Canceled
	}
	sendRestoreNotification = func(ctx context.Context, _ string, _ notification.RestoreResult) error {
		notificationCanceled = ctx.Err() != nil
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"restore-test"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want context.Canceled", err)
	}
	if !restoreCanceled {
		t.Fatal("restore runner did not receive canceled context")
	}
	if notificationCanceled {
		t.Fatal("notification received canceled context")
	}
}

func TestRunForwardsPartialMetricsAndCleanupOutcomes(t *testing.T) {
	t.Setenv(discordNotificationWebhookEnv, "https://discord.example/webhook")
	oldRun, oldNotify := runRestoreTest, sendRestoreNotification
	t.Cleanup(func() { runRestoreTest, sendRestoreNotification = oldRun, oldNotify })
	errRestore := errors.New("private database detail")
	runRestoreTest = func(context.Context, []string) (restore.RunSummary, error) {
		return restore.RunSummary{
			FailedPhase: "verify", DatabaseSizeAttempted: true,
			RecoveryAttempted: true, RecoveryStatus: "success", RecoveryDuration: 2 * time.Second,
			TempCleanupStatus: "error", TempCleanupDuration: time.Second,
		}, errRestore
	}
	called := false
	sendRestoreNotification = func(_ context.Context, _ string, result notification.RestoreResult) error {
		called = true
		if result.Success || result.FailedPhase != "verify" || !result.DatabaseSizeAttempted || result.DatabaseSizeAvailable {
			t.Errorf("incorrect partial result: %+v", result)
		}
		if result.Recovery == nil || result.Recovery.Status != "success" || result.Recovery.Duration != 2*time.Second {
			t.Errorf("recovery not mapped: %+v", result.Recovery)
		}
		if result.TempCleanup == nil || result.TempCleanup.Status != "failure" || result.TempCleanup.Duration != time.Second {
			t.Errorf("temp cleanup not mapped: %+v", result.TempCleanup)
		}
		return nil
	}
	if err := run(context.Background(), []string{"restore-test"}); !errors.Is(err, errRestore) || !called {
		t.Fatalf("run err=%v called=%t", err, called)
	}
}

func TestNotificationFailureDoesNotFailSuccessfulRestore(t *testing.T) {
	t.Setenv(discordNotificationWebhookEnv, "https://discord.example/webhook")
	oldRun, oldNotify := runRestoreTest, sendRestoreNotification
	t.Cleanup(func() { runRestoreTest, sendRestoreNotification = oldRun, oldNotify })
	runRestoreTest = func(context.Context, []string) (restore.RunSummary, error) {
		return restore.RunSummary{Success: true}, nil
	}
	sendRestoreNotification = func(context.Context, string, notification.RestoreResult) error {
		return errors.New("notification failed")
	}
	if err := run(context.Background(), []string{"restore-test"}); err != nil {
		t.Fatal(err)
	}
}
