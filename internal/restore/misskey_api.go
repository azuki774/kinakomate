package restore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
	"github.com/azuki774/kinakomate/internal/log"
)

const (
	misskeyRetryInterval  = 2 * time.Second
	misskeyRequestTimeout = 10 * time.Second
)

// MisskeyAPI describes the Misskey HTTP checks used after a restore.
type MisskeyAPI interface {
	WaitForReadiness(ctx context.Context, cfg *config.Config, timeout time.Duration) error
	CheckGlobalTimeline(ctx context.Context, cfg *config.Config) error
}

type misskeyAPI struct {
	client         *http.Client
	logger         *slog.Logger
	retryInterval  time.Duration
	requestTimeout time.Duration
}

func newMisskeyAPI() *misskeyAPI {
	return &misskeyAPI{
		client: &http.Client{
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		logger:         log.New(),
		retryInterval:  misskeyRetryInterval,
		requestTimeout: misskeyRequestTimeout,
	}
}

func (m *misskeyAPI) WaitForReadiness(ctx context.Context, cfg *config.Config, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		if m.readinessAttempt(waitCtx, cfg) {
			return nil
		}

		timer := time.NewTimer(m.retryInterval)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return fmt.Errorf("wait for Misskey readiness: %w", waitCtx.Err())
		case <-timer.C:
		}
	}
}

func (m *misskeyAPI) readinessAttempt(ctx context.Context, cfg *config.Config) bool {
	requestCtx, cancel := context.WithTimeout(ctx, m.requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, misskeyURL(cfg, "/healthz"), nil)
	if err != nil {
		return false
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close() //nolint:errcheck
	return is2xx(resp.StatusCode)
}

// CheckGlobalTimeline verifies that the restored instance can return valid
// public notes without logging any note content.
func (m *misskeyAPI) CheckGlobalTimeline(ctx context.Context, cfg *config.Config) error {
	requestCtx, cancel := context.WithTimeout(ctx, m.requestTimeout)
	defer cancel()

	body := []byte(`{"limit":10}`)
	req, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		misskeyURL(cfg, "/api/notes/global-timeline"),
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create Misskey global timeline request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		if requestCtx.Err() != nil {
			return fmt.Errorf("request Misskey global timeline: %w", requestCtx.Err())
		}
		return fmt.Errorf("request Misskey global timeline: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if !is2xx(resp.StatusCode) {
		return fmt.Errorf("misskey global timeline returned HTTP status %d", resp.StatusCode)
	}

	count, err := validateGlobalTimeline(requestCtx, resp.Body)
	if err != nil {
		return err
	}
	m.logger.InfoContext(ctx, "Misskey global timeline validated", "count", count)
	return nil
}

func validateGlobalTimeline(ctx context.Context, body io.Reader) (int, error) {
	decoder := json.NewDecoder(body)
	var notes []json.RawMessage
	if err := decoder.Decode(&notes); err != nil {
		if ctx.Err() != nil {
			return 0, fmt.Errorf("read Misskey global timeline response: %w", ctx.Err())
		}
		return 0, fmt.Errorf("decode Misskey global timeline response")
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if ctx.Err() != nil {
			return 0, fmt.Errorf("read Misskey global timeline response: %w", ctx.Err())
		}
		return 0, fmt.Errorf("misskey global timeline response must contain exactly one JSON value")
	}
	if len(notes) == 0 || len(notes) > 10 {
		return 0, fmt.Errorf("misskey global timeline returned an invalid note count")
	}

	for _, rawNote := range notes {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(rawNote, &fields); err != nil {
			return 0, fmt.Errorf("misskey global timeline contains an invalid note")
		}
		id, err := noteString(fields, "id")
		if err != nil || id == "" {
			return 0, fmt.Errorf("misskey global timeline contains an invalid note")
		}
		createdAt, err := noteString(fields, "createdAt")
		if err != nil || createdAt == "" {
			return 0, fmt.Errorf("misskey global timeline contains an invalid note")
		}
		if _, err := time.Parse(time.RFC3339, createdAt); err != nil {
			return 0, fmt.Errorf("misskey global timeline contains an invalid note")
		}
	}
	return len(notes), nil
}

func noteString(fields map[string]json.RawMessage, name string) (string, error) {
	raw, ok := fields[name]
	if !ok {
		return "", fmt.Errorf("missing note field")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func misskeyURL(cfg *config.Config, path string) string {
	return strings.TrimRight(cfg.MisskeyBaseURL, "/") + path
}

func is2xx(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}
