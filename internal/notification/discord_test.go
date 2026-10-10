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
	"unicode/utf16"
	"unicode/utf8"
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

func TestFormatRestoreResultAggregatesTempCleanupFailure(t *testing.T) {
	r := RestoreResult{
		Success: true,
		Total:   time.Hour + 2*time.Minute + 3*time.Second,
		Phases: []Phase{
			{Name: "preflight", Status: "success", Duration: 500 * time.Millisecond},
			{Name: "restore", Status: "failure", Duration: 2 * time.Second},
			{Name: "verify", Status: "skipped"},
			{Name: "cleanup", Status: "success", Duration: time.Second},
		},
		TempCleanup: &Phase{Name: "ignored", Status: "failure", Duration: 2 * time.Second},
		Recovery:    &Phase{Name: "hostile", Status: "success", Duration: 3 * time.Second},
	}
	got := FormatRestoreResult(r)
	for _, want := range []string{"restore-test succeeded", "合計: 1h2m3s", "準備・バックアップ取得: 未実行", "起動・API検証: 未実行", "後処理: 3s（失敗）", "失敗後の後処理: 3s（成功）"} {
		if !strings.Contains(got, want) {
			t.Errorf("formatted output missing %q: %s", want, got)
		}
	}
	// Cleanup success duration (1s) plus temp-file cleanup failure (2s) must
	// aggregate into the cleanup phase and take failure precedence.
	if !strings.Contains(got, "後処理: 3s（失敗）") {
		t.Errorf("temp cleanup failure did not aggregate: %s", got)
	}
	// Only known phase labels are ever rendered; arbitrary DTO names must not leak.
	for _, leaked := range []string{"ignored", "hostile"} {
		if strings.Contains(got, leaked) {
			t.Errorf("untracked phase leaked %q: %s", leaked, got)
		}
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
func TestBackupEmbedStatePrecedence(t *testing.T) {
	cases := []struct {
		name              string
		success, verified bool
		wantColor         int
	}{
		{"success", true, false, colorSuccess},
		{"success and verified", true, true, colorSuccess},
		{"failure before verification", false, false, colorFailure},
		{"verified but failed", false, true, colorAmber},
	}
	descriptions := make(map[string]string, len(cases))
	for _, tc := range cases {
		embed := backupEmbed(BackupResult{Success: tc.success, UploadVerified: tc.verified})
		if embed.Color != tc.wantColor {
			t.Errorf("%s: color = %#x, want %#x", tc.name, embed.Color, tc.wantColor)
		}
		descriptions[tc.name] = embed.Description
	}
	if descriptions["failure before verification"] == descriptions["verified but failed"] {
		t.Error("verified failure must be distinguishable from pre-verification failure")
	}
}

func TestBackupEmbedExactLocationAndFileName(t *testing.T) {
	cases := []struct {
		name         string
		bucket, key  string
		wantLocation string
		wantFile     string
	}{
		{"repeated slash and dotdot", "example-backups", "daily//../example-db.sql.gz", "s3://example-backups/daily//../example-db.sql.gz", "example-db.sql.gz"},
		{"no directory", "b", "plain.dump", "s3://b/plain.dump", "plain.dump"},
		{"trailing slash", "b", "dir/", "s3://b/dir/", ""},
		{"leading slash", "b", "/root.dump", "s3://b//root.dump", "root.dump"},
	}
	for _, tc := range cases {
		embed := backupEmbed(BackupResult{S3Bucket: tc.bucket, S3Key: tc.key})
		loc, _ := embedField(embed, "S3 Location")
		file, _ := embedField(embed, "Backup File")
		if !strings.Contains(loc, "`"+tc.wantLocation+"`") {
			t.Errorf("%s: S3 Location = %q, want exact %q", tc.name, loc, tc.wantLocation)
		}
		if tc.wantFile == "" {
			if file != "未取得" {
				t.Errorf("%s: Backup File = %q, want unknown", tc.name, file)
			}
			continue
		}
		if !strings.Contains(file, "`"+tc.wantFile+"`") {
			t.Errorf("%s: Backup File = %q, want exact %q", tc.name, file, tc.wantFile)
		}
		if tc.wantFile != tc.key && strings.Contains(file, tc.bucket) {
			t.Errorf("%s: Backup File leaked path: %q", tc.name, file)
		}
	}
}

func TestBackupEmbedUnknownVersusZero(t *testing.T) {
	known := backupEmbed(BackupResult{BackupSize: 0, BackupSizeAvailable: true, DatabaseName: "db"})
	if size, _ := embedField(known, "File Size"); !strings.Contains(size, "0 B") {
		t.Errorf("known zero size = %q, want to contain 0 B", size)
	}
	if db, _ := embedField(known, "Database"); !strings.Contains(db, "`db`") {
		t.Errorf("database = %q, want exact db", db)
	}

	unknown := backupEmbed(BackupResult{})
	if size, _ := embedField(unknown, "File Size"); size != "未取得" {
		t.Errorf("unknown size = %q, want 未取得", size)
	}
	if db, _ := embedField(unknown, "Database"); db != "未取得" {
		t.Errorf("unknown database = %q, want 未取得", db)
	}
	if loc, _ := embedField(unknown, "S3 Location"); loc != "未取得" {
		t.Errorf("unknown location = %q, want 未取得", loc)
	}
	if unknown.Timestamp != "" {
		t.Errorf("timestamp = %q, want empty for unknown completion time", unknown.Timestamp)
	}
}

func TestBackupEmbedTimestampIsUTC(t *testing.T) {
	completed := time.Date(2026, 3, 4, 5, 6, 7, 8, time.FixedZone("JST", 9*3600))
	embed := backupEmbed(BackupResult{CompletedAt: completed})
	want := completed.UTC().Format(time.RFC3339Nano)
	if embed.Timestamp != want {
		t.Errorf("timestamp = %q, want %q", embed.Timestamp, want)
	}
	if !strings.HasSuffix(embed.Timestamp, "Z") {
		t.Errorf("timestamp = %q, want UTC Z suffix", embed.Timestamp)
	}
}

func TestBackupEmbedEscapesUnsafeIdentity(t *testing.T) {
	embed := backupEmbed(BackupResult{
		DatabaseName: "db\n`break`@everyone\u0085\u2028\u2029",
		S3Bucket:     "b",
		S3Key:        "k<@123>.dump",
	})
	db, _ := embedField(embed, "Database")
	if strings.ContainsAny(db, "\n\u0085\u2028\u2029") {
		t.Errorf("database value kept raw line separators or controls: %q", db)
	}
	if !strings.Contains(db, "\\n") || !strings.Contains(db, "\\`") {
		t.Errorf("database unsafe characters were not escaped: %q", db)
	}
	if strings.HasPrefix(db, "`") {
		t.Errorf("unsafe database should not be shown as inline code: %q", db)
	}
	file, _ := embedField(embed, "Backup File")
	if !strings.Contains(file, "k<@123>.dump") {
		t.Errorf("filename lost its content: %q", file)
	}

	// A name with markdown but no code-breaking characters is preserved exactly
	// inside a code span.
	safe := backupEmbed(BackupResult{DatabaseName: "prod*db_1"})
	if got, _ := embedField(safe, "Database"); got != "`prod*db_1`" {
		t.Errorf("safe database = %q, want code-wrapped exact name", got)
	}
}

func TestBackupEmbedBoundsEmojiAndTotal(t *testing.T) {
	// 511 emoji render exactly at the 1024-unit field cap when code-wrapped.
	embed := backupEmbed(BackupResult{DatabaseName: strings.Repeat("\U0001F600", 511)})
	db, _ := embedField(embed, "Database")
	if utf16Units(db) != maxFieldValueUTF16 {
		t.Errorf("at-limit emoji units = %d, want %d", utf16Units(db), maxFieldValueUTF16)
	}
	if strings.Contains(db, "…") {
		t.Errorf("at-limit emoji was truncated: %q", db)
	}

	over := strings.Repeat("\U0001F600", 512)
	embed = backupEmbed(BackupResult{DatabaseName: over, S3Bucket: "b", S3Key: over})
	total := utf16Units(embed.Title) + utf16Units(embed.Description)
	for _, f := range embed.Fields {
		if utf16Units(f.Value) > maxFieldValueUTF16 {
			t.Errorf("field %q units = %d, want <= %d", f.Name, utf16Units(f.Value), maxFieldValueUTF16)
		}
		if !utf8.ValidString(f.Value) {
			t.Errorf("field %q is not valid UTF-8: %q", f.Name, f.Value)
		}
		total += utf16Units(f.Name) + utf16Units(f.Value)
	}
	db, _ = embedField(embed, "Database")
	if !strings.Contains(db, "…") {
		t.Errorf("over-limit emoji was not truncated: %q", db)
	}
	if total > 6000 {
		t.Errorf("total embed units = %d, want <= 6000", total)
	}
}

func TestBackupEmbedDoesNotSplitEscapesAtFieldBoundary(t *testing.T) {
	input := strings.Repeat("\\", 511) + "\n"
	exact := backupEmbed(BackupResult{DatabaseName: input})
	got, _ := embedField(exact, "Database")
	if want := strings.Repeat("\\\\", 511) + "\\n"; got != want {
		t.Fatalf("exact-limit escapes = %q, want %q", got, want)
	}
	truncated := backupEmbed(BackupResult{DatabaseName: input + "x"})
	got, _ = embedField(truncated, "Database")
	if want := strings.Repeat("\\\\", 511) + "…"; got != want {
		t.Fatalf("truncation split an escape: got %q, want %q", got, want)
	}
}

func TestBackupEmbedPhaseAllowlistPrivacy(t *testing.T) {
	embed := backupEmbed(BackupResult{
		FailedPhase: "DB_PASSWORD=secret",
		Phases: []Phase{
			{Name: "SQL body secret", Status: "secret status", Duration: time.Second},
			{Name: "upload", Status: "failure", Duration: 2 * time.Second},
			{Name: "dump", Status: "weird", Duration: 3 * time.Second},
		},
	})
	if _, ok := embedField(embed, "失敗フェーズ"); ok {
		t.Error("unknown failed phase was rendered")
	}
	timing, _ := embedField(embed, "処理時間")
	for _, want := range []string{"S3保存: 2s（失敗）", "ダンプ作成: 状態不明", "後処理: 未実行", "合計: <1s"} {
		if !strings.Contains(timing, want) {
			t.Errorf("処理時間 missing %q: %s", want, timing)
		}
	}
	if strings.Contains(timing, "secret") || strings.Contains(timing, "SQL body") || strings.Contains(timing, "weird") {
		t.Errorf("処理時間 leaked untrusted phase values: %s", timing)
	}

	known := backupEmbed(BackupResult{FailedPhase: "cleanup"})
	if label, ok := embedField(known, "失敗フェーズ"); !ok || label != "後処理" {
		t.Errorf("failed phase = (%q, %v), want 後処理", label, ok)
	}
}

func TestSendBackupResultUsesSafeDiscordPayload(t *testing.T) {
	var gotPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type = %q, want application/json", r.Header.Get("Content-Type"))
		}
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

	result := BackupResult{
		Success: true, DatabaseName: "prod-db\n@everyone", S3Bucket: "bucket", S3Key: "dir/file.dump",
		BackupSizeAvailable: true, UploadVerified: true,
		Phases: []Phase{{Name: "upload", Status: "success", Duration: time.Second}},
	}
	if err := SendBackupResult(context.Background(), server.URL, result); err != nil {
		t.Fatalf("SendBackupResult returned error: %v", err)
	}

	// The backup notification carries exactly one embed and no free-form content.
	if content, ok := gotPayload["content"]; ok && content != "" {
		t.Errorf("content = %v, want omitted for backup", content)
	}
	embeds, ok := gotPayload["embeds"].([]any)
	if !ok || len(embeds) != 1 {
		t.Fatalf("embeds = %#v, want one embed", gotPayload["embeds"])
	}
	embed, ok := embeds[0].(map[string]any)
	if !ok {
		t.Fatalf("embed = %T, want object", embeds[0])
	}
	fields, ok := embed["fields"].([]any)
	if !ok {
		t.Fatalf("embed fields = %#v, want array", embed["fields"])
	}
	foundDatabase := false
	for _, f := range fields {
		field, ok := f.(map[string]any)
		if !ok {
			continue
		}
		if field["name"] == "Database" {
			foundDatabase = true
			value, _ := field["value"].(string)
			if strings.Contains(value, "\n") {
				t.Errorf("Database field kept a raw newline: %q", value)
			}
		}
	}
	if !foundDatabase {
		t.Error("missing Database field")
	}

	allowed, ok := gotPayload["allowed_mentions"].(map[string]any)
	if !ok {
		t.Fatalf("allowed_mentions = %T, want object", gotPayload["allowed_mentions"])
	}
	parse, ok := allowed["parse"].([]any)
	if !ok || len(parse) != 0 {
		t.Errorf("allowed_mentions.parse = %v, want empty array", allowed["parse"])
	}
}

// embedField returns the value of the named embed field.
func embedField(embed discordEmbed, name string) (string, bool) {
	for _, f := range embed.Fields {
		if f.Name == name {
			return f.Value, true
		}
	}
	return "", false
}

func utf16Units(value string) int {
	return len(utf16.Encode([]rune(value)))
}
