package restore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

var errGTLResponseRead = errors.New("global timeline response read failed")

// MisskeyAPI describes the Misskey HTTP checks used after a restore.
type MisskeyAPI interface {
	WaitForReadiness(ctx context.Context, cfg *config.Config, timeout time.Duration) error
	CheckGlobalTimeline(ctx context.Context, cfg *config.Config) error
}

type misskeyAPI struct {
	client            *http.Client
	logger            *slog.Logger
	retryInterval     time.Duration
	requestTimeout    time.Duration
	gtlRetryInterval  time.Duration
	gtlRequestTimeout time.Duration
	gtlRetryTimeout   time.Duration
}

func newMisskeyAPI(loggers ...*slog.Logger) *misskeyAPI {
	logger := log.New()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	return &misskeyAPI{
		client: &http.Client{
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		logger:            logger,
		retryInterval:     misskeyRetryInterval,
		requestTimeout:    misskeyRequestTimeout,
		gtlRetryInterval:  config.DefaultGTLRetryInterval,
		gtlRequestTimeout: config.DefaultGTLRequestTimeout,
		gtlRetryTimeout:   config.DefaultGTLRetryTimeout,
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
	requestTimeout, retryInterval, retryTimeout := m.gtlSettings(cfg)
	retryCtx, cancel := context.WithTimeout(ctx, retryTimeout)
	defer cancel()

	for attempt := 1; ; attempt++ {
		if err := retryCtx.Err(); err != nil {
			return fmt.Errorf("check Misskey global timeline: %w", err)
		}

		err, retryable := m.checkGlobalTimelineAttempt(retryCtx, cfg, requestTimeout)
		if err == nil {
			return nil
		}
		// Preserve parent cancellation immediately, even when it happened while
		// the transport was returning its own error.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("check Misskey global timeline: %w", ctxErr)
		}
		if retryCtxErr := retryCtx.Err(); retryCtxErr != nil {
			return fmt.Errorf("check Misskey global timeline retry timeout: %w", retryCtxErr)
		}
		if !retryable {
			return err
		}

		m.logger.WarnContext(ctx, "Misskey global timeline check retrying",
			"attempt", attempt, "retry_interval_seconds", int64(retryInterval/time.Second))
		timer := time.NewTimer(retryInterval)
		select {
		case <-retryCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return fmt.Errorf("check Misskey global timeline retry timeout: %w", retryCtx.Err())
		case <-timer.C:
		}
	}
}

func (m *misskeyAPI) gtlSettings(cfg *config.Config) (time.Duration, time.Duration, time.Duration) {
	requestTimeout := m.gtlRequestTimeout
	retryInterval := m.gtlRetryInterval
	retryTimeout := m.gtlRetryTimeout
	if requestTimeout <= 0 {
		requestTimeout = config.DefaultGTLRequestTimeout
	}
	if retryInterval <= 0 {
		retryInterval = config.DefaultGTLRetryInterval
	}
	if retryTimeout <= 0 {
		retryTimeout = config.DefaultGTLRetryTimeout
	}
	if cfg != nil {
		if cfg.GTLRequestTimeout > 0 {
			requestTimeout = cfg.GTLRequestTimeout
		}
		if cfg.GTLRetryInterval > 0 {
			retryInterval = cfg.GTLRetryInterval
		}
		if cfg.GTLRetryTimeout > 0 {
			retryTimeout = cfg.GTLRetryTimeout
		}
	}
	return requestTimeout, retryInterval, retryTimeout
}

func (m *misskeyAPI) checkGlobalTimelineAttempt(ctx context.Context, cfg *config.Config, requestTimeout time.Duration) (error, bool) {
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	body := []byte(`{"limit":10}`)
	req, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		misskeyURL(cfg, "/api/notes/global-timeline"),
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create Misskey global timeline request: %w", err), false
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			resp.Body.Close() //nolint:errcheck
		}
		if requestCtx.Err() != nil {
			return fmt.Errorf("request Misskey global timeline: %w", requestCtx.Err()), true
		}
		return fmt.Errorf("request Misskey global timeline: %w", err), true
	}
	if resp.StatusCode >= http.StatusInternalServerError && resp.StatusCode < 600 {
		if resp.Body != nil {
			resp.Body.Close() //nolint:errcheck
		}
		return fmt.Errorf("misskey global timeline returned HTTP status %d", resp.StatusCode), true
	}
	if !is2xx(resp.StatusCode) {
		if resp.Body != nil {
			resp.Body.Close() //nolint:errcheck
		}
		return fmt.Errorf("misskey global timeline returned HTTP status %d", resp.StatusCode), false
	}
	if resp.Body == nil {
		return fmt.Errorf("misskey global timeline response has no body"), false
	}
	defer resp.Body.Close() //nolint:errcheck

	count, err := validateGlobalTimeline(requestCtx, &gtlResponseBody{reader: resp.Body})
	if err != nil {
		return err, errors.Is(err, errGTLResponseRead) || errors.Is(err, context.DeadlineExceeded)
	}
	m.logger.InfoContext(ctx, "Misskey global timeline validated", "count", count)
	return nil, false
}

type gtlResponseBody struct {
	reader io.Reader
}

func (r *gtlResponseBody) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		// Do not propagate arbitrary reader errors: a custom transport could
		// include response data in its error text. The marker is enough to
		// classify the failure as retryable without exposing response content.
		return n, errGTLResponseRead
	}
	return n, err
}

func validateGlobalTimeline(ctx context.Context, body io.Reader) (int, error) {
	decoder := json.NewDecoder(body)
	var notes []json.RawMessage
	if err := decoder.Decode(&notes); err != nil {
		if ctx.Err() != nil {
			return 0, fmt.Errorf("read Misskey global timeline response: %w", ctx.Err())
		}
		if errors.Is(err, errGTLResponseRead) {
			return 0, fmt.Errorf("read Misskey global timeline response: %w", err)
		}
		return 0, fmt.Errorf("decode Misskey global timeline response")
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if ctx.Err() != nil {
			return 0, fmt.Errorf("read Misskey global timeline response: %w", ctx.Err())
		}
		if errors.Is(err, errGTLResponseRead) {
			return 0, fmt.Errorf("read Misskey global timeline response: %w", err)
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
