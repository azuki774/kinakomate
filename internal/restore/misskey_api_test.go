package restore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type trackingReadCloser struct {
	io.Reader
	closed atomic.Bool
}

type contextAwareReadCloser struct {
	ctx     context.Context
	started chan<- struct{}
	closed  atomic.Bool
}

func (body *contextAwareReadCloser) Read(_ []byte) (int, error) {
	select {
	case body.started <- struct{}{}:
	default:
	}
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (body *contextAwareReadCloser) Close() error {
	body.closed.Store(true)
	return nil
}

func (body *trackingReadCloser) Close() error {
	body.closed.Store(true)
	return nil
}

func TestMisskeyAPIWaitForReadinessImmediateSuccess(t *testing.T) {
	t.Parallel()

	requests := make(chan *http.Request, 1)
	api := newMisskeyAPI()
	api.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests <- request.Clone(request.Context())
		return response(http.StatusNoContent, ""), nil
	})
	err := api.WaitForReadiness(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"}, time.Second)
	if err != nil {
		t.Fatalf("WaitForReadiness() error = %v", err)
	}

	select {
	case request := <-requests:
		if request.Method != http.MethodGet {
			t.Errorf("method = %q, want %q", request.Method, http.MethodGet)
		}
		if request.URL.Path != "/healthz" {
			t.Errorf("path = %q, want %q", request.URL.Path, "/healthz")
		}
	default:
		t.Fatal("server did not receive a request")
	}
}

func TestMisskeyAPIWaitForReadinessRetriesNon2xx(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	api := newMisskeyAPI()
	api.retryInterval = time.Millisecond
	api.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return response(http.StatusServiceUnavailable, "not ready"), nil
		}
		return response(http.StatusOK, "ready"), nil
	})

	err := api.WaitForReadiness(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"}, time.Second)
	if err != nil {
		t.Fatalf("WaitForReadiness() error = %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestMisskeyAPIWaitForReadinessRetriesTransportError(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	api := newMisskeyAPI()
	api.retryInterval = time.Millisecond
	api.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			return nil, errors.New("connection refused")
		}
		return response(http.StatusOK, "ready"), nil
	})

	err := api.WaitForReadiness(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"}, time.Second)
	if err != nil {
		t.Fatalf("WaitForReadiness() error = %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestMisskeyAPIWaitForReadinessOverallTimeout(t *testing.T) {
	t.Parallel()

	api := newMisskeyAPI()
	api.retryInterval = time.Millisecond
	api.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusServiceUnavailable, "not ready"), nil
	})

	err := api.WaitForReadiness(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"}, 20*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForReadiness() error = %v, want context deadline exceeded", err)
	}
}

func TestMisskeyAPIWaitForReadinessParentCancellation(t *testing.T) {
	t.Parallel()

	firstAttempt := make(chan struct{})
	api := newMisskeyAPI()
	api.retryInterval = time.Hour
	api.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		close(firstAttempt)
		return response(http.StatusServiceUnavailable, "not ready"), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- api.WaitForReadiness(ctx, &config.Config{MisskeyBaseURL: "https://example.test"}, time.Hour)
	}()
	<-firstAttempt
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WaitForReadiness() error = %v, want context canceled", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("WaitForReadiness() did not return promptly after parent cancellation")
	}
}

func TestMisskeyAPICheckGlobalTimelinePostsAndValidatesNotes(t *testing.T) {
	t.Parallel()

	requests := make(chan *http.Request, 1)
	api := newMisskeyAPI()
	api.requestTimeout = 50 * time.Millisecond
	api.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests <- request.Clone(request.Context())
		return response(http.StatusOK, `[{"id":"note-1","createdAt":"2026-09-05T12:00:00Z"}]`), nil
	})

	err := api.CheckGlobalTimeline(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"})
	if err != nil {
		t.Fatalf("CheckGlobalTimeline() error = %v", err)
	}

	select {
	case request := <-requests:
		if request.Method != http.MethodPost {
			t.Errorf("method = %q, want %q", request.Method, http.MethodPost)
		}
		if request.URL.Path != "/api/notes/global-timeline" {
			t.Errorf("path = %q, want %q", request.URL.Path, "/api/notes/global-timeline")
		}
		if contentType := request.Header.Get("Content-Type"); contentType != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", contentType)
		}
		var body struct {
			Limit int `json:"limit"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body.Limit != 10 {
			t.Errorf("limit = %d, want 10", body.Limit)
		}
		deadline, ok := request.Context().Deadline()
		if !ok {
			t.Error("request has no deadline")
		} else if until := time.Until(deadline); until <= 0 || until > api.requestTimeout {
			t.Errorf("request deadline is in %v, want within (0, %v]", until, api.requestTimeout)
		}
	default:
		t.Fatal("server did not receive a request")
	}
}

func TestMisskeyAPIClosesResponseBodies(t *testing.T) {
	t.Parallel()

	readinessBody := &trackingReadCloser{Reader: strings.NewReader("")}
	timelineBody := &trackingReadCloser{Reader: strings.NewReader(`[{"id":"note-1","createdAt":"2026-09-05T12:00:00Z"}]`)}
	api := newMisskeyAPI()
	var requests atomic.Int32
	api.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		if requests.Add(1) == 1 {
			return &http.Response{StatusCode: http.StatusOK, Body: readinessBody}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: timelineBody}, nil
	})

	if err := api.WaitForReadiness(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"}, time.Second); err != nil {
		t.Fatalf("WaitForReadiness() error = %v", err)
	}
	if err := api.CheckGlobalTimeline(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"}); err != nil {
		t.Fatalf("CheckGlobalTimeline() error = %v", err)
	}
	if !readinessBody.closed.Load() {
		t.Error("readiness response body was not closed")
	}
	if !timelineBody.closed.Load() {
		t.Error("timeline response body was not closed")
	}
}

func TestMisskeyAPIWaitForReadinessAppliesRequestTimeoutToEveryAttempt(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	api := newMisskeyAPI()
	api.retryInterval = time.Millisecond
	api.requestTimeout = 5 * time.Millisecond
	api.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if attempts.Add(1) == 1 {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}
		return response(http.StatusOK, ""), nil
	})

	err := api.WaitForReadiness(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"}, time.Second)
	if err != nil {
		t.Fatalf("WaitForReadiness() error = %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestMisskeyAPIConstructorDoesNotFollowRedirectsOrDisableTLSVerification(t *testing.T) {
	t.Parallel()

	api := newMisskeyAPI()
	if api.retryInterval != 2*time.Second {
		t.Errorf("retryInterval = %v, want 2s", api.retryInterval)
	}
	if api.requestTimeout != 10*time.Second {
		t.Errorf("requestTimeout = %v, want 10s", api.requestTimeout)
	}
	if api.client.Transport != nil {
		t.Fatal("client Transport is configured; want the secure default transport")
	}
	if err := api.client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect() error = %v, want http.ErrUseLastResponse", err)
	}
}

func TestMisskeyAPICheckGlobalTimelineRejectsInvalidResponsesWithoutLeakingBody(t *testing.T) {
	t.Parallel()

	validNote := `{"id":"note-1","createdAt":"2026-09-05T12:00:00Z"}`
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"non-2xx", http.StatusServiceUnavailable, "private note body"},
		{"invalid JSON", http.StatusOK, "private note body"},
		{"non-array", http.StatusOK, validNote},
		{"empty array", http.StatusOK, "[]"},
		{"too many notes", http.StatusOK, "[" + strings.TrimSuffix(strings.Repeat(validNote+",", 11), ",") + "]"},
		{"trailing JSON value", http.StatusOK, "[" + validNote + "] []"},
		{"missing id", http.StatusOK, `[{"createdAt":"2026-09-05T12:00:00Z"}]`},
		{"wrong id type", http.StatusOK, `[{"id":1,"createdAt":"2026-09-05T12:00:00Z"}]`},
		{"empty id", http.StatusOK, `[{"id":"","createdAt":"2026-09-05T12:00:00Z"}]`},
		{"missing createdAt", http.StatusOK, `[{"id":"note-1"}]`},
		{"wrong createdAt type", http.StatusOK, `[{"id":"note-1","createdAt":1}]`},
		{"empty createdAt", http.StatusOK, `[{"id":"note-1","createdAt":""}]`},
		{"invalid createdAt", http.StatusOK, `[{"id":"note-1","createdAt":"private note body"}]`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := newMisskeyAPI()
			api.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return response(test.status, test.body), nil
			})

			err := api.CheckGlobalTimeline(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"})
			if err == nil {
				t.Fatal("CheckGlobalTimeline() error = nil, want validation error")
			}
			if strings.Contains(err.Error(), "private note body") {
				t.Fatalf("CheckGlobalTimeline() error leaked response body: %v", err)
			}
		})
	}
}

func TestMisskeyAPICheckGlobalTimelineRequiresExactNoteKeyNames(t *testing.T) {
	t.Parallel()

	tests := []string{
		`[{"ID":"note-1","createdAt":"2026-09-05T12:00:00Z"}]`,
		`[{"id":"note-1","CREATEDAT":"2026-09-05T12:00:00Z"}]`,
	}
	for _, body := range tests {
		api := newMisskeyAPI()
		api.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return response(http.StatusOK, body), nil
		})

		err := api.CheckGlobalTimeline(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"})
		if err == nil {
			t.Fatalf("CheckGlobalTimeline() accepted note with non-exact keys: %s", body)
		}
	}
}

func TestMisskeyAPICheckGlobalTimelineRequiresStrictRFC3339CreatedAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		createdAt string
		wantErr   bool
	}{
		{"one digit hour", "2026-09-05T1:00:00Z", true},
		{"comma fractional separator", "2026-09-05T12:00:00,123Z", true},
		{"out of range offset", "2026-09-05T12:00:00+24:00", true},
		{"second beyond leap second", "1990-12-31T23:59:61Z", true},
		{"fractional seconds", "2026-09-05T12:00:00.123456789Z", false},
		{"legal offset", "2026-09-05T12:00:00+09:30", false},
		{"lowercase t and z", "2026-09-05t12:00:00z", false},
		{"leap second", "1990-12-31T23:59:60Z", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api := newMisskeyAPI()
			body := `[{"id":"note-1","createdAt":"` + test.createdAt + `"}]`
			api.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return response(http.StatusOK, body), nil
			})

			err := api.CheckGlobalTimeline(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"})
			if test.wantErr && err == nil {
				t.Fatal("CheckGlobalTimeline() accepted non-RFC3339 createdAt")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("CheckGlobalTimeline() error = %v", err)
			}
		})
	}
}

func TestMisskeyAPICheckGlobalTimelineAcceptsExactlyTenNotes(t *testing.T) {
	t.Parallel()

	note := `{"id":"note-1","createdAt":"2026-09-05T12:00:00Z"}`
	body := "[" + strings.TrimSuffix(strings.Repeat(note+",", 10), ",") + "]"
	api := newMisskeyAPI()
	api.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, body), nil
	})

	if err := api.CheckGlobalTimeline(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"}); err != nil {
		t.Fatalf("CheckGlobalTimeline() error = %v", err)
	}
}

func TestMisskeyAPICheckGlobalTimelineReturnsRequestDeadlineDuringBodyRead(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 1)
	api := newMisskeyAPI()
	api.requestTimeout = 5 * time.Millisecond
	api.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &contextAwareReadCloser{ctx: request.Context(), started: started},
		}, nil
	})

	result := make(chan error, 1)
	go func() {
		result <- api.CheckGlobalTimeline(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"})
	}()
	select {
	case <-started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeline response body was not read")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("CheckGlobalTimeline() error = %v, want context deadline exceeded", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("CheckGlobalTimeline() did not return promptly after request timeout")
	}
}

func TestMisskeyAPICheckGlobalTimelineReturnsParentCancellationDuringBodyRead(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 1)
	api := newMisskeyAPI()
	api.requestTimeout = time.Hour
	api.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       &contextAwareReadCloser{ctx: request.Context(), started: started},
		}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- api.CheckGlobalTimeline(ctx, &config.Config{MisskeyBaseURL: "https://example.test"})
	}()
	<-started
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CheckGlobalTimeline() error = %v, want context canceled", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("CheckGlobalTimeline() did not return promptly after parent cancellation")
	}
}

func TestMisskeyAPICheckGlobalTimelineAllowsUnknownFieldsAndLogsOnlyCount(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	api := newMisskeyAPI()
	api.logger = slog.New(slog.NewTextHandler(&logs, nil))
	api.client.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, `[{"id":"note-1","createdAt":"2026-09-05T12:00:00Z","text":"private note body","extra":{"value":1}}]`), nil
	})

	err := api.CheckGlobalTimeline(context.Background(), &config.Config{MisskeyBaseURL: "https://example.test"})
	if err != nil {
		t.Fatalf("CheckGlobalTimeline() error = %v", err)
	}
	output := logs.String()
	if !strings.Contains(output, "Misskey global timeline validated") || !strings.Contains(output, "count=1") {
		t.Fatalf("success log = %q, want static message and count", output)
	}
	if strings.Contains(output, "private note body") || strings.Contains(output, "note-1") {
		t.Fatalf("success log leaked note data: %q", output)
	}
}
