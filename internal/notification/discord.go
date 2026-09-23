// Package notification sends the small set of notifications emitted by
// kinakomate. It deliberately accepts only a run status so operational details
// and restore errors cannot be included in a webhook message by accident.
package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"
)

const requestTimeout = 10 * time.Second

var errDiscordWebhook = errors.New("discord notification failed")

type discordPayload struct {
	Content         string          `json:"content"`
	AllowedMentions allowedMentions `json:"allowed_mentions"`
}

type allowedMentions struct {
	Parse []string `json:"parse"`
}

// SendRestoreResult sends a generic restore-test result to a Discord webhook.
// The caller is responsible for providing a context that is independent of
// the restore operation when a notification should still be attempted after
// that operation is canceled.
func SendRestoreResult(ctx context.Context, webhookURL string, success bool) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	parsed, err := url.Parse(webhookURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errDiscordWebhook
	}

	payload := discordPayload{
		Content: "restore-test failed",
		AllowedMentions: allowedMentions{
			Parse: []string{},
		},
	}
	if success {
		payload.Content = "restore-test succeeded"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errDiscordWebhook
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		// Do not return the request error: net/http includes the complete URL in
		// several request errors, and the webhook URL is a secret.
		return errDiscordWebhook
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		// Transport errors can contain the webhook URL. Keep the returned error
		// deliberately generic so it is safe to log at the command boundary.
		return errDiscordWebhook
	}
	defer resp.Body.Close() //nolint:errcheck // the response body is not used

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return errDiscordWebhook
	}
	return nil
}
