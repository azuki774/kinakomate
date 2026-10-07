// Package notification sends the small set of notifications emitted by kinakomate.
package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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

// RestoreResult contains only metrics and fixed state identifiers, never raw errors.
type RestoreResult struct {
	Success               bool
	FailedPhase           string
	DatabaseSize          int64
	DatabaseSizeAttempted bool
	DatabaseSizeAvailable bool
	BackupSize            int64
	BackupSizeAvailable   bool
	Total                 time.Duration
	Phases                []Phase
	Recovery              *Phase
	TempCleanup           *Phase
}

// BackupResult contains only metrics and fixed state identifiers, never raw errors.
type BackupResult struct {
	Success             bool
	FailedPhase         string
	BackupSize          int64
	BackupSizeAvailable bool
	UploadVerified      bool
	Total               time.Duration
	Phases              []Phase
}

type Phase struct {
	Name, Status string
	Duration     time.Duration
}

// SendRestoreResult sends a generic restore-test result to a Discord webhook.
// The caller is responsible for providing a context that is independent of
// the restore operation when a notification should still be attempted after
// that operation is canceled.
func SendRestoreResult(ctx context.Context, webhookURL string, result RestoreResult) error {
	return sendDiscordResult(ctx, webhookURL, FormatRestoreResult(result))
}

// SendBackupResult sends a generic backup result to a Discord webhook.
func SendBackupResult(ctx context.Context, webhookURL string, result BackupResult) error {
	return sendDiscordResult(ctx, webhookURL, FormatBackupResult(result))
}

func sendDiscordResult(ctx context.Context, webhookURL, content string) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	parsed, err := url.Parse(webhookURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errDiscordWebhook
	}

	payload := discordPayload{
		Content: content,
		AllowedMentions: allowedMentions{
			Parse: []string{},
		},
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

// FormatRestoreResult formats a bounded message using only known phase labels.
func FormatRestoreResult(r RestoreResult) string {
	state := "failed"
	if r.Success {
		state = "succeeded"
	}
	lines := []string{"restore-test " + state}
	labels := map[string]string{"preflight": "事前確認", "prepare": "準備・バックアップ取得", "restore": "DB復元", "verify": "起動・API検証", "cleanup": "後処理"}
	if label, ok := labels[r.FailedPhase]; ok {
		lines = append(lines, "失敗フェーズ: "+label)
	}
	dbSize := sizeText(r.DatabaseSize, r.DatabaseSizeAvailable)
	if r.DatabaseSizeAttempted && !r.DatabaseSizeAvailable {
		dbSize = "取得不可"
	}
	lines = append(lines, "DBサイズ（復元直後）: "+dbSize, "S3バックアップ（gzip）: "+sizeText(r.BackupSize, r.BackupSizeAvailable), "合計: "+durationText(r.Total))
	phaseByName := make(map[string]Phase, len(labels))
	for _, p := range r.Phases {
		if _, ok := labels[p.Name]; ok {
			phaseByName[p.Name] = p
		}
	}
	for _, name := range []string{"preflight", "prepare", "restore", "verify", "cleanup"} {
		p, ok := phaseByName[name]
		if !ok {
			lines = append(lines, labels[name]+": 未実行")
			continue
		}
		if name == "cleanup" && p.Status != "skipped" && r.TempCleanup != nil {
			p.Duration += r.TempCleanup.Duration
			if r.TempCleanup.Status == "failure" {
				p.Status = "failure"
			}
		}
		lines = append(lines, labels[name]+": "+phaseText(p))
	}
	if r.Recovery != nil {
		text := phaseText(*r.Recovery)
		if r.Recovery.Status == "success" {
			text += "（成功）"
		}
		lines = append(lines, "失敗後の後処理: "+text)
	}
	if r.TempCleanup != nil && (phaseByName["cleanup"].Name == "" || phaseByName["cleanup"].Status == "skipped") {
		lines = append(lines, "一時ファイル削除: "+phaseText(*r.TempCleanup))
	}
	content := strings.Join(lines, "\n")
	// Keep well below Discord's 2000-character limit, even for hostile DTO values.
	if len([]rune(content)) > 1800 {
		content = string([]rune(content)[:1797]) + "..."
	}
	return content
}

// FormatBackupResult formats a bounded message using only known phase labels.
func FormatBackupResult(r BackupResult) string {
	state := "failed"
	if r.Success {
		state = "succeeded"
	}
	lines := []string{"backup " + state}
	labels := map[string]string{
		"preflight": "事前確認",
		"dump":      "ダンプ作成",
		"validate":  "gzip検証",
		"upload":    "S3保存",
		"cleanup":   "後処理",
	}
	if label, ok := labels[r.FailedPhase]; ok {
		lines = append(lines, "失敗フェーズ: "+label)
	}
	verified := "未確認"
	if r.UploadVerified {
		verified = "確認済み"
	}
	lines = append(lines,
		"gzipサイズ: "+sizeText(r.BackupSize, r.BackupSizeAvailable),
		"S3保存確認: "+verified,
		"合計: "+durationText(r.Total),
	)

	phaseByName := make(map[string]Phase, len(labels))
	for _, p := range r.Phases {
		if _, ok := labels[p.Name]; ok {
			phaseByName[p.Name] = p
		}
	}
	for _, name := range []string{"preflight", "dump", "validate", "upload", "cleanup"} {
		p, ok := phaseByName[name]
		if !ok {
			lines = append(lines, labels[name]+": 未実行")
			continue
		}
		lines = append(lines, labels[name]+": "+backupPhaseText(p))
	}

	content := strings.Join(lines, "\n")
	// Keep well below Discord's 2000-character limit.
	if len([]rune(content)) > 1800 {
		content = string([]rune(content)[:1797]) + "..."
	}
	return content
}

func backupPhaseText(p Phase) string {
	switch p.Status {
	case "success":
		return durationText(p.Duration)
	case "failure":
		return durationText(p.Duration) + "（失敗）"
	case "skipped":
		return "未実行"
	default:
		return "状態不明"
	}
}

func phaseText(p Phase) string {
	if p.Status == "skipped" {
		return "未実行"
	}
	text := durationText(p.Duration)
	if p.Status == "failure" {
		text += "（失敗）"
	}
	return text
}
func sizeText(n int64, ok bool) string {
	if !ok {
		return "未取得"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.2f %s", v, units[i])
}
func durationText(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	d = d.Round(time.Second)
	seconds := int64(d / time.Second)
	h := seconds / 3600
	m := (seconds % 3600) / 60
	s := seconds % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
