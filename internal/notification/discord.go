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
	"unicode"
)

const requestTimeout = 10 * time.Second

var errDiscordWebhook = errors.New("discord notification failed")

type discordPayload struct {
	Content         string          `json:"content,omitempty"`
	Embeds          []discordEmbed  `json:"embeds,omitempty"`
	AllowedMentions allowedMentions `json:"allowed_mentions"`
}

type discordEmbed struct {
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	Color       int            `json:"color,omitempty"`
	Fields      []discordField `json:"fields,omitempty"`
	Timestamp   string         `json:"timestamp,omitempty"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
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
	DatabaseName        string
	S3Bucket            string
	S3Key               string
	CompletedAt         time.Time
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
	return sendDiscordResult(ctx, webhookURL, discordPayload{Content: FormatRestoreResult(result)})
}

// SendBackupResult sends a single embed describing the backup result to a
// Discord webhook. It never sends free-form content.
func SendBackupResult(ctx context.Context, webhookURL string, result BackupResult) error {
	return sendDiscordResult(ctx, webhookURL, discordPayload{Embeds: []discordEmbed{backupEmbed(result)}})
}

func sendDiscordResult(ctx context.Context, webhookURL string, payload discordPayload) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	parsed, err := url.Parse(webhookURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errDiscordWebhook
	}

	// Mentions are disabled centrally for every notification, including the
	// restore text notification.
	payload.AllowedMentions = allowedMentions{Parse: []string{}}
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

const (
	backupEmbedTitle = "PostgreSQL Backup Notification"

	colorSuccess = 0x57F287 // green
	colorAmber   = 0xFEE75C // amber: verified upload but failed afterwards
	colorFailure = 0xED4245 // red

	// Three bounded identity fields plus fixed labels and numeric metrics keep
	// the entire embed below Discord's 6000-unit limit.
	maxFieldValueUTF16 = 1024
)

var (
	backupPhaseLabels = map[string]string{
		"preflight": "事前確認",
		"dump":      "ダンプ作成",
		"validate":  "gzip検証",
		"upload":    "S3保存",
		"cleanup":   "後処理",
	}
	backupPhaseOrder = []string{"preflight", "dump", "validate", "upload", "cleanup"}
)

// backupEmbed builds the single embed sent for a backup result. All dynamic
// identity values are bounded and escaped; fixed phase labels are the only
// phase text ever rendered.
func backupEmbed(r BackupResult) discordEmbed {
	embed := discordEmbed{
		Title:       backupEmbedTitle,
		Description: backupDescription(r),
		Color:       backupColor(r),
	}
	if !r.CompletedAt.IsZero() {
		embed.Timestamp = r.CompletedAt.UTC().Format(time.RFC3339Nano)
	}

	add := func(name, value string) {
		embed.Fields = append(embed.Fields, discordField{Name: name, Value: value, Inline: false})
	}

	add("Database", identityValue(r.DatabaseName))
	add("Backup File", identityValue(backupFileName(r.S3Key)))
	add("S3 Location", identityValue(s3Location(r.S3Bucket, r.S3Key)))
	add("File Size", sizeText(r.BackupSize, r.BackupSizeAvailable))

	verified := "未確認"
	if r.UploadVerified {
		verified = "確認済み"
	}
	add("S3保存確認", verified)

	if label, ok := backupPhaseLabels[r.FailedPhase]; ok {
		add("失敗フェーズ", label)
	}
	add("処理時間", backupTimingText(r))
	return embed
}

// backupColor selects the embed colour from state precedence:
// success is green, a verified-but-failed run is amber, and any failure
// before verification is red.
func backupColor(r BackupResult) int {
	switch {
	case r.Success:
		return colorSuccess
	case r.UploadVerified:
		return colorAmber
	default:
		return colorFailure
	}
}

func backupDescription(r BackupResult) string {
	switch {
	case r.Success:
		return "バックアップは成功しました"
	case r.UploadVerified:
		return "S3への保存は確認済みですが、バックアップは失敗しました"
	default:
		return "バックアップは失敗しました"
	}
}

// backupTimingText renders the total and the five known phase timings/statuses.
func backupTimingText(r BackupResult) string {
	lines := []string{"合計: " + durationText(r.Total)}
	phaseByName := make(map[string]Phase, len(backupPhaseOrder))
	for _, p := range r.Phases {
		if _, ok := backupPhaseLabels[p.Name]; ok {
			phaseByName[p.Name] = p
		}
	}
	for _, name := range backupPhaseOrder {
		p, ok := phaseByName[name]
		if !ok {
			lines = append(lines, backupPhaseLabels[name]+": 未実行")
			continue
		}
		lines = append(lines, backupPhaseLabels[name]+": "+backupPhaseText(p))
	}
	return strings.Join(lines, "\n")
}

// backupFileName returns the substring after the last slash of the S3 key
// without any path cleaning, so the displayed name matches what was stored.
func backupFileName(key string) string {
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		return key[i+1:]
	}
	return key
}

// s3Location renders the exact object location, never a normalized path.
func s3Location(bucket, key string) string {
	if bucket == "" || key == "" {
		return ""
	}
	return "s3://" + bucket + "/" + key
}

// identityValue renders an optional identity string, using a fixed marker when
// the value is unknown so that missing data is never rendered as a real value.
func identityValue(s string) string {
	if s == "" {
		return "未取得"
	}
	return renderBounded(s)
}

// renderBounded keeps complete escaped characters and code delimiters within
// the field limit without first allocating the full, possibly huge, input.
func renderBounded(s string) string {
	safe := isSafeCode(s)
	mark := ""
	limit := maxFieldValueUTF16
	if safe {
		mark = "`"
		limit -= 2
	}

	var b strings.Builder
	b.Grow(min(len(s), maxFieldValueUTF16) + len(mark)*2)
	b.WriteString(mark)
	units, cut := 0, b.Len()
	for _, r := range s {
		fragment := renderIdentityRune(r, safe)
		n := runeUTF16Units(r)
		if !safe && fragment[0] == '\\' {
			n = len(fragment) // Escapes contain only ASCII.
		}
		if units+n > limit {
			return b.String()[:cut] + "…" + mark
		}
		b.WriteString(fragment)
		units += n
		if units <= limit-1 {
			cut = b.Len()
		}
	}
	b.WriteString(mark)
	return b.String()
}

// isSafeCode reports whether s can be shown as inline code without breaking out
// of the code span or introducing a line break.
func isSafeCode(s string) bool {
	for _, r := range s {
		if r == '`' || unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

// renderIdentityRune produces the display fragment for one rune. Inside a code
// span the rune is literal; otherwise markdown metacharacters and control
// characters are escaped so a hostile name cannot alter the message.
func renderIdentityRune(r rune, safe bool) string {
	if safe {
		return string(r)
	}
	switch r {
	case '\\', '`', '*', '_', '~', '|', '>', '<', '[', ']', '#', '(', ')':
		return "\\" + string(r)
	case '\n':
		return "\\n"
	case '\r':
		return "\\r"
	case '\t':
		return "\\t"
	}
	if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
		return fmt.Sprintf("\\u%04x", r)
	}
	return string(r)
}

func runeUTF16Units(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
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
