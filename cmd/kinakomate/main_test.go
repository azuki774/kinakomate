package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"testing"
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

			runRestoreTest = func(context.Context, []string) error { return tc.restoreErr }
			var gotURL string
			var gotSuccess bool
			sendRestoreNotification = func(ctx context.Context, webhookURL string, success bool) error {
				if ctx.Err() != nil {
					t.Errorf("notification context is canceled: %v", ctx.Err())
				}
				gotURL = webhookURL
				gotSuccess = success
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
	sendRestoreNotification = func(context.Context, string, bool) error {
		notifications++
		return nil
	}
	runRestoreTest = func(context.Context, []string) error { return nil }
	if err := run(context.Background(), []string{"restore-test"}); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	t.Setenv(discordNotificationWebhookEnv, "https://discord.example/webhook")
	runRestoreTest = func(context.Context, []string) error { return flag.ErrHelp }
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
	sendRestoreNotification = func(context.Context, string, bool) error {
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
	runRestoreTest = func(context.Context, []string) error { return restoreErr }
	sendRestoreNotification = func(context.Context, string, bool) error {
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
	runRestoreTest = func(ctx context.Context, _ []string) error {
		restoreCanceled = errors.Is(ctx.Err(), context.Canceled)
		return context.Canceled
	}
	sendRestoreNotification = func(ctx context.Context, _ string, _ bool) error {
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
