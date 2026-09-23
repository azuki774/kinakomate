package notification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendRestoreResultPayload(t *testing.T) {
	for _, tc := range []struct {
		name    string
		success bool
		content string
	}{
		{name: "success", success: true, content: "restore-test succeeded"},
		{name: "failure", success: false, content: "restore-test failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotContentType string
			var gotPayload map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotContentType = r.Header.Get("Content-Type")
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
					return
				}
				if err := json.Unmarshal(body, &gotPayload); err != nil {
					t.Errorf("decode request body: %v", err)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			if err := SendRestoreResult(context.Background(), server.URL, tc.success); err != nil {
				t.Fatalf("SendRestoreResult returned error: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotContentType != "application/json" {
				t.Errorf("content type = %q, want application/json", gotContentType)
			}
			if gotPayload["content"] != tc.content {
				t.Errorf("content = %v, want %q", gotPayload["content"], tc.content)
			}
			allowed, ok := gotPayload["allowed_mentions"].(map[string]any)
			if !ok {
				t.Fatalf("allowed_mentions = %T, want object", gotPayload["allowed_mentions"])
			}
			parse, ok := allowed["parse"].([]any)
			if !ok {
				t.Fatalf("allowed_mentions.parse = %T, want array", allowed["parse"])
			}
			if len(parse) != 0 {
				t.Errorf("allowed_mentions.parse = %v, want empty", parse)
			}
		})
	}
}

func TestSendRestoreResultRejectsNon2xxAndDoesNotFollowRedirect(t *testing.T) {
	var finalRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			finalRequests++
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, "/final", http.StatusFound)
	}))
	defer server.Close()

	webhookURL := server.URL + "/webhook-secret?token=top-secret"
	err := SendRestoreResult(context.Background(), webhookURL, false)
	if err == nil {
		t.Fatal("expected redirect response to fail")
	}
	if finalRequests != 0 {
		t.Fatalf("redirect target received %d requests, want 0", finalRequests)
	}
	if strings.Contains(err.Error(), "webhook-secret") || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("error leaked webhook URL: %v", err)
	}
}

func TestSendRestoreResultTransportErrorDoesNotLeakWebhookURL(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	webhookURL := server.URL + "/secret-path?token=top-secret"
	server.Close()

	err := SendRestoreResult(context.Background(), webhookURL, true)
	if !errors.Is(err, errDiscordWebhook) {
		t.Fatalf("error = %v, want discord notification error", err)
	}
	if strings.Contains(err.Error(), "secret-path") || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("error leaked webhook URL: %v", err)
	}
}

func TestSendRestoreResultRejectsNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	if err := SendRestoreResult(context.Background(), server.URL, true); err == nil {
		t.Fatal("expected non-2xx response to fail")
	}
}
