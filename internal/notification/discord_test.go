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
	"time"
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

			if err := SendRestoreResult(context.Background(), server.URL, RestoreResult{Success: tc.success}); err != nil {
				t.Fatalf("SendRestoreResult returned error: %v", err)
			}
			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotContentType != "application/json" {
				t.Errorf("content type = %q, want application/json", gotContentType)
			}
			if content, ok := gotPayload["content"].(string); !ok || !strings.HasPrefix(content, tc.content) {
				t.Errorf("content = %v, want prefix %q", gotPayload["content"], tc.content)
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

func TestFormatRestoreResult(t *testing.T) {
	r := RestoreResult{Success: true, DatabaseSize: 1536, DatabaseSizeAvailable: true, BackupSize: 1024 * 1024, BackupSizeAvailable: true, Total: time.Hour + 2*time.Minute + 3*time.Second,
		Phases:      []Phase{{Name: "preflight", Status: "success", Duration: 500 * time.Millisecond}, {Name: "restore", Status: "failure", Duration: 2 * time.Second}, {Name: "verify", Status: "skipped"}, {Name: "cleanup", Status: "success", Duration: time.Second}},
		TempCleanup: &Phase{Name: "ignored", Status: "failure", Duration: 2 * time.Second}, Recovery: &Phase{Name: "hostile", Status: "success", Duration: 3 * time.Second}}
	got := FormatRestoreResult(r)
	want := "restore-test succeeded\nDBサイズ（復元直後）: 1.50 KiB\nS3バックアップ（gzip）: 1.00 MiB\n合計: 1h2m3s\n事前確認: <1s\n準備・バックアップ取得: 未実行\nDB復元: 2s（失敗）\n起動・API検証: 未実行\n後処理: 3s（失敗）\n失敗後の後処理: 3s（成功）"
	if got != want {
		t.Fatalf("formatted output:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "hostile") || strings.Contains(got, "ignored") || strings.Contains(got, "success") && strings.Contains(got, "preflight: success") {
		t.Fatal("arbitrary DTO values leaked")
	}
}

func TestFormatRestoreResultDatabaseSizeAndBounds(t *testing.T) {
	for _, tc := range []struct {
		attempted, available bool
		want                 string
	}{{false, false, "未取得"}, {true, false, "取得不可"}, {true, true, "0 B"}} {
		got := FormatRestoreResult(RestoreResult{DatabaseSizeAttempted: tc.attempted, DatabaseSizeAvailable: tc.available})
		if !strings.Contains(got, "DBサイズ（復元直後）: "+tc.want) {
			t.Errorf("want DB text %q in %q", tc.want, got)
		}
	}
	long := RestoreResult{FailedPhase: strings.Repeat("x", 10000), Phases: []Phase{{Name: "cleanup", Status: strings.Repeat("z", 10000)}}}
	if got := FormatRestoreResult(long); len([]rune(got)) > 1800 {
		t.Fatalf("output length = %d", len([]rune(got)))
	}
}

func TestFormatRestoreResultSkippedCleanupKeepsFileDeletionSeparate(t *testing.T) {
	for _, status := range []string{"success", "failure"} {
		got := FormatRestoreResult(RestoreResult{
			FailedPhase: "restore",
			Phases:      []Phase{{Name: "cleanup", Status: "skipped"}},
			TempCleanup: &Phase{Status: status, Duration: 2 * time.Second},
			Recovery:    &Phase{Status: "failure", Duration: time.Second},
		})
		for _, want := range []string{"失敗フェーズ: DB復元", "\n後処理: 未実行", "一時ファイル削除: 2s", "失敗後の後処理: 1s（失敗）"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in %s", want, got)
			}
		}
		if status == "failure" && !strings.Contains(got, "一時ファイル削除: 2s（失敗）") {
			t.Errorf("missing file deletion failure in %s", got)
		}
	}
}

func TestMetricUnits(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{
		{0, "0 B"}, {1023, "1023 B"}, {1024, "1.00 KiB"},
		{1 << 20, "1.00 MiB"}, {1 << 30, "1.00 GiB"}, {1 << 40, "1.00 TiB"},
	} {
		if got := sizeText(tc.n, true); got != tc.want {
			t.Errorf("size %d = %s, want %s", tc.n, got, tc.want)
		}
	}
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "<1s"}, {999 * time.Millisecond, "<1s"}, {time.Second, "1s"},
		{time.Minute + 2*time.Second, "1m2s"}, {time.Hour, "1h0m0s"},
	} {
		if got := durationText(tc.d); got != tc.want {
			t.Errorf("duration %s = %s, want %s", tc.d, got, tc.want)
		}
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
	err := SendRestoreResult(context.Background(), webhookURL, RestoreResult{})
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

	err := SendRestoreResult(context.Background(), webhookURL, RestoreResult{Success: true})
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

	if err := SendRestoreResult(context.Background(), server.URL, RestoreResult{Success: true}); err == nil {
		t.Fatal("expected non-2xx response to fail")
	}
}
