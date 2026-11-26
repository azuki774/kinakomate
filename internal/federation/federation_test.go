package federation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azuki774/kinakomate/internal/config"
	kinakolog "github.com/azuki774/kinakomate/internal/log"
)

func fixedNow() time.Time {
	return time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
}

func newTestConfig(baseURL, outputPath string) *config.FederationReportConfig {
	return &config.FederationReportConfig{
		MisskeyBaseURL:   baseURL,
		OutputPath:       outputPath,
		RequestTimeout:   2 * time.Second,
		NewWindow:        168 * time.Hour,
		NewWindowHours:   168,
		ActiveWindow:     7 * 24 * time.Hour,
		ActiveWindowDays: 7,
	}
}

// assertSafeValue enforces the sanitizer guarantee: only printable ASCII that
// cannot open markup, break a code span, or split a table row may survive.
func assertSafeValue(t *testing.T, value string) {
	t.Helper()
	for _, r := range value {
		if r < 0x20 || r > 0x7e {
			t.Fatalf("value %q contains non-printable/control rune %U", value, r)
		}
		switch r {
		case '`', '|', '<', '>', '[', ']', '(', ')', '\'', '"', '\\':
			t.Fatalf("value %q contains markup rune %q", value, r)
		}
	}
}

func TestSanitizeHost(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "host and port", raw: "misskey.example:443", want: "misskey.example:443"},
		{name: "uppercase ascii lowercased", raw: "Misskey.Example:443", want: "misskey.example:443"},
		{name: "trimmed", raw: "  example.com  ", want: "example.com"},
		{name: "empty", raw: " \t\n ", want: "unknown"},
		{
			name: "markup",
			raw:  "evil`host|name\n<script>[x](y)\u202e\x00",
			want: "evil?host?name??script??x??y???",
		},
		{
			name: "long",
			raw:  strings.Repeat("a", maxHostLength+2),
			want: strings.Repeat("a", maxHostLength-3) + "...",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeHost(tt.raw)
			if got != tt.want {
				t.Fatalf("SanitizeHost(%q) = %q, want %q", tt.raw, got, tt.want)
			}
			assertSafeValue(t, got)
		})
	}
}

func TestSanitizeSoftwareName(t *testing.T) {
	tests := []struct {
		name string
		raw  *string
		want string
	}{
		{name: "nil", raw: nil, want: "unknown"},
		{name: "empty", raw: new("   "), want: "unknown"},
		{name: "allowed", raw: new("misskey-v1.2+build"), want: "misskey-v1.2+build"},
		{name: "uppercase ascii lowercased", raw: new("Misskey-Fork"), want: "misskey-fork"},
		{name: "markup", raw: new("a`b|c\nd<e>f[g](h)\u202e"), want: "a_b_c_d_e_f_g__h__"},
		{
			name: "long",
			raw:  new(strings.Repeat("m", maxSoftwareNameLength+8)),
			want: strings.Repeat("m", maxSoftwareNameLength-3) + "...",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeSoftwareName(tt.raw)
			if got != tt.want {
				t.Fatalf("SanitizeSoftwareName() = %q, want %q", got, tt.want)
			}
			assertSafeValue(t, got)
		})
	}
}

func TestSanitizeSoftwareVersion(t *testing.T) {
	tests := []struct {
		name string
		raw  *string
		want string
	}{
		{name: "nil", raw: nil, want: "unknown"},
		{name: "empty", raw: new(""), want: "unknown"},
		{
			name: "allowed",
			raw:  new("4.2.0-RC1+build:2026_11"),
			want: "4.2.0-RC1+build:2026_11",
		},
		{name: "markup", raw: new("a`b|c\nd"), want: "a_b_c_d"},
		{
			name: "long",
			raw:  new(strings.Repeat("v", maxSoftwareVersionLength+10)),
			want: strings.Repeat("v", maxSoftwareVersionLength-3) + "...",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeSoftwareVersion(tt.raw)
			if got != tt.want {
				t.Fatalf("SanitizeSoftwareVersion() = %q, want %q", got, tt.want)
			}
			assertSafeValue(t, got)
		})
	}
}

func TestSanitizersStripEveryMarkupVector(t *testing.T) {
	hostile := "`|<>\n\r\t[]()\u202a\u202e\u2066\x00\x1b\u200f"
	values := []string{
		SanitizeHost(hostile),
		SanitizeSoftwareName(&hostile),
		SanitizeSoftwareVersion(&hostile),
	}
	for _, value := range values {
		assertSafeValue(t, value)
	}
	if values[0] != strings.Repeat("?", 17) {
		t.Fatalf("SanitizeHost(hostile) = %q", values[0])
	}
}

func TestHTTPClientFetchMeta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/meta" {
			t.Errorf("path = %q, want /api/meta", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var request metaRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.Detail {
			t.Error("detail = true, want false")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"version":"2026.1.0","name":"<script>alert(1)</script>","description":"`+"`evil`"+`"}`)
	}))
	defer server.Close()

	cfg := newTestConfig(server.URL, "unused")
	meta, err := NewHTTPClient().FetchMeta(context.Background(), cfg)
	if err != nil {
		t.Fatalf("FetchMeta: %v", err)
	}
	if meta.Version != "2026.1.0" {
		t.Fatalf("version = %q", meta.Version)
	}
}

func makeRecords(offset, count int) []InstanceRecord {
	records := make([]InstanceRecord, 0, count)
	for i := range count {
		host := fmt.Sprintf("host-%03d", offset+i)
		records = append(records, InstanceRecord{
			Host:            host,
			UsersCount:      int64(offset + i),
			NotesCount:      int64(offset+i) * 2,
			SoftwareName:    new("mastodon"),
			SoftwareVersion: new("4.2.0"),
		})
	}
	return records
}

func TestHTTPClientFetchInstancesPaginationAndDeduplication(t *testing.T) {
	firstRetrieved := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)

	page0 := makeRecords(0, instancesPageLimit)
	page0[1].FirstRetrievedAt = new(firstRetrieved)

	page1 := makeRecords(instancesPageLimit, instancesPageLimit)
	page1[0].Host = " HOST-000 "
	page1[0].UsersCount = 999

	page2 := makeRecords(2*instancesPageLimit, 5)
	page2[0].Host = "host-199"
	page2[0].UsersCount = 777
	page2 = append(page2, InstanceRecord{Host: "   "})

	pages := map[int][]InstanceRecord{
		0:                      page0,
		instancesPageLimit:     page1,
		2 * instancesPageLimit: page2,
	}
	var requestsMu sync.Mutex
	var requests []instancesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/federation/instances" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var request instancesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requestsMu.Lock()
		requests = append(requests, request)
		requestsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pages[request.Offset])
	}))
	defer server.Close()

	cfg := newTestConfig(server.URL, "unused")
	records, err := NewHTTPClient().FetchInstances(context.Background(), cfg)
	if err != nil {
		t.Fatalf("FetchInstances: %v", err)
	}
	requestsMu.Lock()
	gotRequests := append([]instancesRequest(nil), requests...)
	requestsMu.Unlock()

	wantOffsets := []int{0, instancesPageLimit, 2 * instancesPageLimit}
	if len(gotRequests) != len(wantOffsets) {
		t.Fatalf("requests = %d, want %d", len(gotRequests), len(wantOffsets))
	}
	for i, request := range gotRequests {
		if request.Offset != wantOffsets[i] {
			t.Errorf("request %d offset = %d, want %d", i, request.Offset, wantOffsets[i])
		}
		if request.Limit != instancesPageLimit {
			t.Errorf("request %d limit = %d", i, request.Limit)
		}
		if request.Sort != "+firstRetrievedAt" {
			t.Errorf("request %d sort = %q", i, request.Sort)
		}
	}

	if len(records) != 203 {
		t.Fatalf("records = %d, want 203", len(records))
	}
	if records[0].Host != " HOST-000 " || records[0].UsersCount != 999 {
		t.Fatalf("latest duplicate not preserved: %+v", records[0])
	}
	if records[1].FirstRetrievedAt == nil || !records[1].FirstRetrievedAt.Equal(firstRetrieved) {
		t.Fatalf("timestamp not decoded: %+v", records[1].FirstRetrievedAt)
	}
	foundHost199 := false
	for _, record := range records {
		if strings.EqualFold(strings.TrimSpace(record.Host), "host-199") {
			foundHost199 = true
			if record.UsersCount != 777 {
				t.Errorf("host-199 usersCount = %d, want latest 777", record.UsersCount)
			}
		}
		if strings.TrimSpace(record.Host) == "" {
			t.Error("empty host was not skipped")
		}
	}
	if !foundHost199 {
		t.Fatal("host-199 missing")
	}
}

func TestHTTPClientRejectsRedirects(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		_, _ = io.WriteString(w, `{"version":"bad"}`)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
	defer redirector.Close()

	cfg := newTestConfig(redirector.URL, "unused")
	_, err := NewHTTPClient().FetchMeta(context.Background(), cfg)
	if !errors.Is(err, errResponseStatus) {
		t.Fatalf("error = %v, want errResponseStatus", err)
	}
	if reached.Load() {
		t.Fatal("redirect target was requested")
	}
}

func TestHTTPClientRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
	}))
	defer server.Close()

	cfg := newTestConfig(server.URL, "unused")
	_, err := NewHTTPClient().FetchMeta(context.Background(), cfg)
	if !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("error = %v, want errResponseTooLarge", err)
	}
}

func TestHTTPClientRejectsBadResponsesAndConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{
			name: "non-2xx",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			want: errResponseStatus,
		},
		{
			name: "invalid json",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "not json")
			},
			want: errResponseDecode,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()
			cfg := newTestConfig(server.URL, "unused")
			_, err := NewHTTPClient().FetchMeta(context.Background(), cfg)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}

	if _, err := NewHTTPClient().FetchMeta(context.Background(), nil); !errors.Is(err, errClientConfiguration) {
		t.Fatalf("nil config error = %v, want errClientConfiguration", err)
	}
	empty := newTestConfig("", "unused")
	if _, err := NewHTTPClient().FetchMeta(context.Background(), empty); !errors.Is(err, errClientConfiguration) {
		t.Fatalf("empty base URL error = %v, want errClientConfiguration", err)
	}
}

func TestHTTPClientPaginationLimit(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request instancesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(makeRecords(request.Offset, instancesPageLimit))
	}))
	defer server.Close()

	cfg := newTestConfig(server.URL, "unused")
	_, err := NewHTTPClient().FetchInstances(context.Background(), cfg)
	if !errors.Is(err, errPaginationLimit) {
		t.Fatalf("error = %v, want errPaginationLimit", err)
	}
	if got := requestCount.Load(); got != maxPages {
		t.Fatalf("requests = %d, want %d", got, maxPages)
	}
}

func TestHTTPClientPerRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = io.WriteString(w, `{"version":"slow"}`)
	}))
	defer server.Close()

	cfg := newTestConfig(server.URL, "unused")
	cfg.RequestTimeout = 20 * time.Millisecond
	_, err := NewHTTPClient().FetchMeta(context.Background(), cfg)
	if !errors.Is(err, errRequestFailed) {
		t.Fatalf("error = %v, want errRequestFailed", err)
	}
}

func TestAggregateOverviewAndDistributions(t *testing.T) {
	now := fixedNow()
	cfg := newTestConfig("https://misskey.example.com:443", "unused")
	instances := []InstanceRecord{
		{
			Host:                    "a.example",
			UsersCount:              10,
			NotesCount:              100,
			FollowingCount:          5,
			FollowersCount:          5,
			SoftwareName:            new("mastodon"),
			SoftwareVersion:         new("4.2.0"),
			FirstRetrievedAt:        new(now.Add(-time.Hour)),
			LatestRequestReceivedAt: new(now.Add(-time.Hour)),
		},
		{
			Host:                    "b.example",
			UsersCount:              20,
			NotesCount:              200,
			FollowingCount:          10,
			FollowersCount:          10,
			SoftwareName:            new("mastodon"),
			SoftwareVersion:         new("4.2.0"),
			FirstRetrievedAt:        new(now.Add(-30 * 24 * time.Hour)),
			LatestRequestReceivedAt: new(now.Add(-30 * 24 * time.Hour)),
		},
		{
			Host:                    "c.example",
			UsersCount:              5,
			NotesCount:              50,
			FollowingCount:          100,
			FollowersCount:          200,
			SoftwareName:            new("misskey"),
			SoftwareVersion:         new("2026.1.0"),
			FirstRetrievedAt:        new(now.Add(-2 * time.Hour)),
			LatestRequestReceivedAt: new(now.Add(-50 * 24 * time.Hour)),
			IsNotResponding:         true,
		},
		{
			Host:             "d.example",
			SoftwareName:     new("misskey"),
			SoftwareVersion:  new("2026.1.0"),
			IsNotResponding:  true,
			FirstRetrievedAt: new(now.Add(-300 * time.Hour)),
		},
		{
			Host:                    "e.example",
			UsersCount:              999,
			NotesCount:              999,
			SoftwareName:            new("sharkey"),
			SoftwareVersion:         new("1.0.0"),
			LatestRequestReceivedAt: new(now.Add(-time.Hour)),
			IsSuspended:             true,
		},
		{
			Host:                    "f.example",
			UsersCount:              999,
			NotesCount:              999,
			SoftwareName:            new("blocked"),
			SoftwareVersion:         new("1.0.0"),
			LatestRequestReceivedAt: new(now.Add(-time.Hour)),
			IsBlocked:               true,
		},
		{
			Host:                    "g.example",
			UsersCount:              -5,
			NotesCount:              -10,
			FollowingCount:          -3,
			FollowersCount:          -4,
			SoftwareName:            new("Evil`Software"),
			SoftwareVersion:         new("1.0|beta"),
			FirstRetrievedAt:        new(now.Add(-time.Hour)),
			LatestRequestReceivedAt: new(now),
		},
	}
	for i := 1; i <= 6; i++ {
		instances = append(instances, InstanceRecord{
			Host:                    fmt.Sprintf("sw%d.example", i),
			SoftwareName:            new(fmt.Sprintf("sw%d", i)),
			SoftwareVersion:         new("1.0"),
			LatestRequestReceivedAt: new(now.Add(-30 * 24 * time.Hour)),
		})
	}

	report := Aggregate(now, cfg, MetaResponse{Version: "2026.1.0"}, instances)

	if report.TargetHost != "misskey.example.com:443" {
		t.Errorf("TargetHost = %q", report.TargetHost)
	}
	if report.SelfVersion != "2026.1.0" {
		t.Errorf("SelfVersion = %q", report.SelfVersion)
	}
	if !report.GeneratedAt.Equal(now) {
		t.Errorf("GeneratedAt = %v", report.GeneratedAt)
	}

	wantOverview := Overview{
		TotalCount:         13,
		ActiveCount:        2,
		NotRespondingCount: 2,
		SuspendedCount:     1,
		BlockedCount:       1,
	}
	if report.Overview != wantOverview {
		t.Fatalf("Overview = %+v, want %+v", report.Overview, wantOverview)
	}

	software := report.SoftwareInstances
	if len(software) != 11 {
		t.Fatalf("software instances = %d, want 11", len(software))
	}
	wantSoftware := []SoftwareInstance{
		{Host: "g.example", SoftwareName: "evil_software", SoftwareVersion: "1.0_beta"},
		{Host: "a.example", SoftwareName: "mastodon", SoftwareVersion: "4.2.0"},
		{Host: "b.example", SoftwareName: "mastodon", SoftwareVersion: "4.2.0"},
		{Host: "c.example", SoftwareName: "misskey", SoftwareVersion: "2026.1.0"},
		{Host: "d.example", SoftwareName: "misskey", SoftwareVersion: "2026.1.0"},
		{Host: "sw1.example", SoftwareName: "sw1", SoftwareVersion: "1.0"},
		{Host: "sw2.example", SoftwareName: "sw2", SoftwareVersion: "1.0"},
		{Host: "sw3.example", SoftwareName: "sw3", SoftwareVersion: "1.0"},
		{Host: "sw4.example", SoftwareName: "sw4", SoftwareVersion: "1.0"},
		{Host: "sw5.example", SoftwareName: "sw5", SoftwareVersion: "1.0"},
		{Host: "sw6.example", SoftwareName: "sw6", SoftwareVersion: "1.0"},
	}
	for i, want := range wantSoftware {
		if software[i] != want {
			t.Errorf("software instance %d = %+v, want %+v", i, software[i], want)
		}
	}

	if report.NewInstances.Total != 3 {
		t.Fatalf("new total = %d, want 3", report.NewInstances.Total)
	}
	wantNewHosts := []string{"a.example", "c.example", "g.example"}
	for i, item := range report.NewInstances.Items {
		if item.Host != wantNewHosts[i] {
			t.Errorf("new item %d host = %q, want %q", i, item.Host, wantNewHosts[i])
		}
	}
	gNew := report.NewInstances.Items[2]
	if gNew.UsersCount != 0 || gNew.NotesCount != 0 {
		t.Errorf("negative counters not clamped: %+v", gNew)
	}

	unhealthy := report.UnhealthyInstances
	if unhealthy.UnhealthyTotal != 2 || unhealthy.UnhealthyWithFFCount != 1 {
		t.Fatalf("unhealthy = %+v", unhealthy)
	}
	if len(unhealthy.Items) != 2 || unhealthy.Items[0].Host != "c.example" || unhealthy.Items[1].Host != "d.example" {
		t.Fatalf("unhealthy order = %+v", unhealthy.Items)
	}
	if unhealthy.Items[0].LatestRequestReceivedAt == nil || unhealthy.Items[1].LatestRequestReceivedAt != nil {
		t.Fatalf("unhealthy timestamps not preserved: %+v", unhealthy.Items)
	}

	top := report.TopInstances
	if len(top) != MaxListedTopInstances {
		t.Fatalf("top = %d, want %d", len(top), MaxListedTopInstances)
	}
	if top[0].Host != "c.example" {
		t.Errorf("top[0] = %q, want c.example", top[0].Host)
	}
	for _, item := range top {
		if item.Host == "e.example" || item.Host == "f.example" {
			t.Errorf("excluded instance %q reached top list", item.Host)
		}
		if item.Host == "g.example" && (item.UsersCount != 0 || item.NotesCount != 0 || item.FollowingCount != 0 || item.FollowersCount != 0) {
			t.Errorf("negative counters not clamped in top list: %+v", item)
		}
		if item.Host == "sw6.example" {
			t.Error("top list exceeded its cap")
		}
	}
}

func TestAggregateSoftwareOrdering(t *testing.T) {
	now := fixedNow()
	cfg := newTestConfig("https://misskey.example.com", "unused")
	instances := []InstanceRecord{
		{Host: "b.example", SoftwareName: new("misskey"), SoftwareVersion: new("2026.1.0")},
		{Host: "a.example", SoftwareName: new("misskey"), SoftwareVersion: new("2026.1.0")},
		{Host: "c.example", SoftwareName: new("misskey"), SoftwareVersion: new("2025.12.0")},
		{Host: "d.example", SoftwareName: new("mastodon"), SoftwareVersion: new("4.2.0")},
		{Host: "e.example", SoftwareName: new("misskey"), SoftwareVersion: new("2026.1.0"), IsSuspended: true},
		{Host: "f.example", SoftwareName: new("misskey"), SoftwareVersion: new("2026.1.0"), IsBlocked: true},
	}

	report := Aggregate(now, cfg, MetaResponse{}, instances)
	want := []SoftwareInstance{
		{Host: "d.example", SoftwareName: "mastodon", SoftwareVersion: "4.2.0"},
		{Host: "c.example", SoftwareName: "misskey", SoftwareVersion: "2025.12.0"},
		{Host: "a.example", SoftwareName: "misskey", SoftwareVersion: "2026.1.0"},
		{Host: "b.example", SoftwareName: "misskey", SoftwareVersion: "2026.1.0"},
	}
	if len(report.SoftwareInstances) != len(want) {
		t.Fatalf("instances = %d, want %d: %+v", len(report.SoftwareInstances), len(want), report.SoftwareInstances)
	}
	for i, item := range want {
		if report.SoftwareInstances[i] != item {
			t.Errorf("instance %d = %+v, want %+v", i, report.SoftwareInstances[i], item)
		}
	}
}

func TestAggregateNewInstanceOrderingAndCompleteness(t *testing.T) {
	now := fixedNow()
	cfg := newTestConfig("https://misskey.example.com", "unused")

	ordering := []InstanceRecord{
		{Host: "h1.example", UsersCount: 1, NotesCount: 10, FirstRetrievedAt: new(now.Add(-time.Hour))},
		{Host: "h2.example", UsersCount: 1, NotesCount: 10, FirstRetrievedAt: new(now.Add(-2 * time.Hour))},
		{Host: "h3.example", UsersCount: 1, NotesCount: 10, FirstRetrievedAt: new(now.Add(-time.Hour))},
	}
	report := Aggregate(now, cfg, MetaResponse{}, ordering)
	got := make([]string, 0, len(report.NewInstances.Items))
	for _, item := range report.NewInstances.Items {
		got = append(got, item.Host)
	}
	if strings.Join(got, ",") != "h1.example,h3.example,h2.example" {
		t.Fatalf("new order = %v", got)
	}

	instances := make([]InstanceRecord, 0, 25)
	for i := range 25 {
		instances = append(instances, InstanceRecord{
			Host:             fmt.Sprintf("n%02d.example", i),
			UsersCount:       int64(i),
			NotesCount:       int64(i),
			FirstRetrievedAt: new(now.Add(-time.Duration(i) * time.Hour)),
		})
	}
	report = Aggregate(now, cfg, MetaResponse{}, instances)
	if report.NewInstances.Total != 25 {
		t.Fatalf("new total = %d, want 25", report.NewInstances.Total)
	}
	if len(report.NewInstances.Items) != 25 {
		t.Fatalf("listed new = %d, want 25", len(report.NewInstances.Items))
	}
	if report.NewInstances.Items[24].Host != "n00.example" {
		t.Errorf("last new = %q, want n00.example", report.NewInstances.Items[24].Host)
	}
	if report.NewInstances.Items[0].Host != "n24.example" {
		t.Errorf("first new = %q, want n24.example", report.NewInstances.Items[0].Host)
	}
}

func TestAggregateUnhealthyOrderingAndCompleteness(t *testing.T) {
	now := fixedNow()
	cfg := newTestConfig("https://misskey.example.com", "unused")
	instances := []InstanceRecord{
		{
			Host:                    "withff-low.example",
			FollowingCount:          1,
			IsNotResponding:         true,
			LatestRequestReceivedAt: new(now.Add(-5 * 24 * time.Hour)),
		},
		{
			Host:                    "withff-high.example",
			FollowersCount:          10,
			IsNotResponding:         true,
			LatestRequestReceivedAt: new(now.Add(-10 * 24 * time.Hour)),
		},
		{
			Host:            "nil-latest.example",
			IsNotResponding: true,
		},
		{
			Host:                    "old-withff.example",
			FollowingCount:          100,
			IsNotResponding:         true,
			LatestRequestReceivedAt: new(now.Add(-24 * time.Hour)),
		},
	}
	for i := range 18 {
		instances = append(instances, InstanceRecord{
			Host:                    fmt.Sprintf("z%02d.example", i),
			IsNotResponding:         true,
			LatestRequestReceivedAt: new(now.Add(-time.Duration(i+1) * time.Hour)),
		})
	}

	report := Aggregate(now, cfg, MetaResponse{}, instances)
	unhealthy := report.UnhealthyInstances
	if unhealthy.UnhealthyTotal != 22 || unhealthy.UnhealthyWithFFCount != 3 {
		t.Fatalf("unhealthy totals = %+v", unhealthy)
	}
	if len(unhealthy.Items) != 22 {
		t.Fatalf("items = %d, want 22", len(unhealthy.Items))
	}
	wantPrefix := []string{
		"old-withff.example",
		"withff-high.example",
		"withff-low.example",
		"nil-latest.example",
		"z17.example",
		"z16.example",
	}
	for i, want := range wantPrefix {
		if unhealthy.Items[i].Host != want {
			t.Errorf("unhealthy[%d] = %q, want %q", i, unhealthy.Items[i].Host, want)
		}
	}
	if unhealthy.Items[19].Host != "z02.example" {
		t.Errorf("unhealthy[19] = %q, want z02.example", unhealthy.Items[19].Host)
	}
}

func TestAggregateTopOrdering(t *testing.T) {
	now := fixedNow()
	cfg := newTestConfig("https://misskey.example.com", "unused")
	instances := []InstanceRecord{
		{Host: "b.example", NotesCount: 10, UsersCount: 1},
		{Host: "a.example", NotesCount: 10, UsersCount: 1},
		{Host: "c.example", NotesCount: 20},
		{Host: "d.example", NotesCount: 1, FollowingCount: 5},
		{Host: "e.example", FollowingCount: 100, IsBlocked: true},
		{Host: "f.example", FollowingCount: 100, IsSuspended: true},
	}

	report := Aggregate(now, cfg, MetaResponse{}, instances)
	got := make([]string, 0, len(report.TopInstances))
	for _, item := range report.TopInstances {
		got = append(got, item.Host)
	}
	if strings.Join(got, ",") != "d.example,c.example,a.example,b.example" {
		t.Fatalf("top order = %v", got)
	}
}

func TestAggregateSanitizesHostileHost(t *testing.T) {
	now := fixedNow()
	cfg := newTestConfig("https://misskey.example.com", "unused")
	instances := []InstanceRecord{{
		Host:                    "evil`host|<b>\n.example",
		SoftwareName:            new("x`y"),
		SoftwareVersion:         new("1|2"),
		FirstRetrievedAt:        new(now),
		LatestRequestReceivedAt: new(now),
	}}
	report := Aggregate(now, cfg, MetaResponse{Version: "v`1<2>"}, instances)
	if report.SelfVersion != "v_1_2_" {
		t.Errorf("SelfVersion = %q", report.SelfVersion)
	}
	if report.NewInstances.Items[0].Host != "evil?host??b??.example" {
		t.Errorf("host = %q", report.NewInstances.Items[0].Host)
	}
	if report.NewInstances.Items[0].SoftwareName != "x_y" || report.NewInstances.Items[0].SoftwareVersion != "1_2" {
		t.Errorf("software = %+v", report.NewInstances.Items[0])
	}
}

func sampleReport() Report {
	now := fixedNow()
	return Report{
		TargetHost:       "target.example",
		SelfVersion:      "2026.1.0",
		GeneratedAt:      now,
		NewWindow:        168 * time.Hour,
		NewWindowHours:   168,
		ActiveWindow:     7 * 24 * time.Hour,
		ActiveWindowDays: 7,
		Overview: Overview{
			TotalCount:         10,
			ActiveCount:        4,
			NotRespondingCount: 2,
			SuspendedCount:     1,
			BlockedCount:       1,
		},
		SoftwareInstances: []SoftwareInstance{
			{Host: "one.example", SoftwareName: "mastodon", SoftwareVersion: "4.2.0"},
			{Host: "two.example", SoftwareName: "mastodon", SoftwareVersion: "4.1.0"},
		},
		NewInstances: NewInstances{
			Total: 1,
			Items: []NewInstance{{
				Host:             "new.example",
				SoftwareName:     "mastodon",
				SoftwareVersion:  "4.2.0",
				UsersCount:       10,
				NotesCount:       100,
				FirstRetrievedAt: now.Add(-time.Hour),
			}},
		},
		UnhealthyInstances: UnhealthyInstances{
			UnhealthyTotal:       1,
			UnhealthyWithFFCount: 1,
			Items: []UnhealthyInstance{{
				Host:                    "sick.example",
				SoftwareName:            "misskey",
				SoftwareVersion:         "2026.1.0",
				UsersCount:              5,
				NotesCount:              50,
				FollowingCount:          2,
				FollowersCount:          3,
				LatestRequestReceivedAt: new(now.Add(-48 * time.Hour)),
			}},
		},
		TopInstances: []TopInstance{{
			Host:            "top.example",
			SoftwareName:    "mastodon",
			SoftwareVersion: "4.2.0",
			UsersCount:      100,
			NotesCount:      1000,
			FollowingCount:  10,
			FollowersCount:  20,
		}},
	}
}

func TestRenderMarkdownStructure(t *testing.T) {
	markdown := RenderMarkdown(sampleReport())

	for _, want := range []string{
		"# 連合レポート: `target.example`",
		"- 対象サーバー: `target.example`",
		"- サーバーバージョン: `2026.1.0`",
		"- 生成日時 (UTC): `2026-11-22T12:00:00Z`",
		"- 新規連合ウィンドウ: `168h`",
		"- アクティブ判定ウィンドウ: `7d`",
		"## 1. 全体サマリ",
		"## 2. ソフトウェア・バージョン別インスタンス一覧 (停止・ブロック除く)",
		"## 3. 新規連合 (直近168h: 1件)",
		"## 4. 要注意: 応答なし・未停止 (計1件 / うちFF関係あり1件)",
		"## 5. 連合規模 Top 10 (停止・ブロック除く / FF数順)",
		"| 連合先総数 | 10 |",
		"| アクティブ | 4 |",
		"| `one.example` | `mastodon` | `4.2.0` |",
		"| `two.example` | `mastodon` | `4.1.0` |",
		"| `new.example` | `mastodon` | `4.2.0` | 10 | 100 | `2026-11-22T11:00:00Z` |",
		"| `sick.example` | `misskey` | `2026.1.0` | 5 | 5 | 50 | `2026-11-20T12:00:00Z` |",
		"| `top.example` | `mastodon` | `4.2.0` | 30 | 100 | 1000 |",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("markdown missing %q\n%s", want, markdown)
		}
	}
}

func TestRenderMarkdownEmptySections(t *testing.T) {
	report := sampleReport()
	report.NewInstances = NewInstances{}
	report.UnhealthyInstances = UnhealthyInstances{}
	markdown := RenderMarkdown(report)
	if strings.Count(markdown, "該当なし") != 2 {
		t.Fatalf("該当なし count = %d\n%s", strings.Count(markdown, "該当なし"), markdown)
	}
	if strings.Contains(markdown, "...ほか") {
		t.Fatalf("unexpected truncation notice:\n%s", markdown)
	}
}

func TestRenderMarkdownSanitizesHostileAggregate(t *testing.T) {
	now := fixedNow()
	cfg := newTestConfig("https://misskey.example.com", "unused")
	instances := []InstanceRecord{{
		Host:                    "evil`host|<script>\n.example",
		SoftwareName:            new("bad`name|<b>"),
		SoftwareVersion:         new("1`|2\n"),
		FollowingCount:          2,
		FirstRetrievedAt:        new(now),
		LatestRequestReceivedAt: new(now),
		IsNotResponding:         true,
	}}
	markdown := RenderMarkdown(Aggregate(now, cfg, MetaResponse{Version: "<script>`v`"}, instances))

	for _, unwanted := range []string{"<script>", "`host", "bad`name", "1`|2", "\u202e", "\x00"} {
		if strings.Contains(markdown, unwanted) {
			t.Errorf("markdown contains hostile fragment %q\n%s", unwanted, markdown)
		}
	}
	if !strings.Contains(markdown, "`evil?host??script??.example`") {
		t.Errorf("sanitized host missing:\n%s", markdown)
	}
	if !strings.Contains(markdown, "`bad_name__b_`") {
		t.Errorf("sanitized software name missing:\n%s", markdown)
	}
}

type stubClient struct {
	meta         MetaResponse
	instances    []InstanceRecord
	metaErr      error
	instancesErr error
}

func (c stubClient) FetchMeta(context.Context, *config.FederationReportConfig) (MetaResponse, error) {
	return c.meta, c.metaErr
}

func (c stubClient) FetchInstances(context.Context, *config.FederationReportConfig) ([]InstanceRecord, error) {
	return c.instances, c.instancesErr
}

func successDependencies(cfg *config.FederationReportConfig, client Client, now time.Time, write func(string, string) error) federationDependencies {
	return federationDependencies{
		loadConfig:     func() (*config.FederationReportConfig, error) { return cfg, nil },
		newClient:      func() Client { return client },
		aggregate:      Aggregate,
		renderMarkdown: RenderMarkdown,
		writeReport:    write,
		now:            func() time.Time { return now },
	}
}

func TestRunWithDependenciesSuccess(t *testing.T) {
	now := fixedNow()
	outputPath := filepath.Join(t.TempDir(), "report.md")
	cfg := newTestConfig("https://misskey.example.com", outputPath)
	instances := []InstanceRecord{
		{
			Host:                    "one.example",
			SoftwareName:            new("mastodon"),
			SoftwareVersion:         new("4.2.0"),
			FirstRetrievedAt:        new(now.Add(-time.Hour)),
			LatestRequestReceivedAt: new(now.Add(-time.Hour)),
		},
		{
			Host:             "two.example",
			SoftwareName:     new("misskey"),
			SoftwareVersion:  new("2026.1.0"),
			IsNotResponding:  true,
			FirstRetrievedAt: new(now.Add(-time.Hour)),
			FollowingCount:   1,
		},
	}

	var writtenPath, writtenContent string
	deps := successDependencies(cfg, stubClient{meta: MetaResponse{Version: "2026.1.0"}, instances: instances}, now,
		func(path, content string) error {
			writtenPath, writtenContent = path, content
			return nil
		})

	var logOutput bytes.Buffer
	summary, err := runWithDependencies(context.Background(), nil, deps, kinakolog.NewWithWriter(&logOutput))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if summary.FailedPhase != "" {
		t.Errorf("FailedPhase = %q", summary.FailedPhase)
	}
	if summary.OutputPath != outputPath {
		t.Errorf("OutputPath = %q", summary.OutputPath)
	}
	wantCounts := RunSummary{
		TotalInstances:          2,
		ActiveInstances:         1,
		NotRespondingInstances:  1,
		NewInstancesCount:       2,
		UnhealthyInstancesCount: 1,
		UnhealthyWithFFCount:    1,
	}
	if summary.TotalInstances != wantCounts.TotalInstances ||
		summary.ActiveInstances != wantCounts.ActiveInstances ||
		summary.NotRespondingInstances != wantCounts.NotRespondingInstances ||
		summary.NewInstancesCount != wantCounts.NewInstancesCount ||
		summary.UnhealthyInstancesCount != wantCounts.UnhealthyInstancesCount ||
		summary.UnhealthyWithFFCount != wantCounts.UnhealthyWithFFCount {
		t.Errorf("summary counts = %+v, want %+v", summary, wantCounts)
	}
	for _, phase := range summary.Phases {
		if phase.Status != "success" {
			t.Errorf("phase %q status = %q", phase.Name, phase.Status)
		}
	}
	if writtenPath != outputPath {
		t.Errorf("written path = %q", writtenPath)
	}
	if !strings.Contains(writtenContent, "# 連合レポート: `misskey.example.com`") {
		t.Errorf("written content missing header:\n%s", writtenContent)
	}

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(logOutput.Bytes()), &entry); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	if entry["msg"] != "federation-report final report" || entry["result"] != "success" {
		t.Errorf("log entry = %+v", entry)
	}
	if entry["output_path"] != outputPath {
		t.Errorf("log output_path = %v", entry["output_path"])
	}
	if entry["total_instances"] != float64(2) || entry["unhealthy_with_ff_count"] != float64(1) {
		t.Errorf("log counts = %+v", entry)
	}
	if _, ok := entry["failed_phase"]; ok {
		t.Errorf("unexpected failed_phase in %+v", entry)
	}
	if _, ok := entry["phases"].([]any); !ok {
		t.Errorf("phases missing from %+v", entry)
	}
}

func TestRunWithDependenciesPhaseFailures(t *testing.T) {
	now := fixedNow()
	outputPath := filepath.Join(t.TempDir(), "report.md")
	cfg := newTestConfig("https://misskey.example.com", outputPath)

	tests := []struct {
		name      string
		deps      func() federationDependencies
		wantPhase string
		wantErr   string
	}{
		{
			name: "config failure",
			deps: func() federationDependencies {
				deps := successDependencies(cfg, stubClient{}, now, func(string, string) error {
					t.Error("write must not run")
					return nil
				})
				deps.loadConfig = func() (*config.FederationReportConfig, error) {
					return nil, errors.New("required input MISSKEY_BASE_URL is missing or empty")
				}
				return deps
			},
			wantPhase: "preflight",
			wantErr:   "required input MISSKEY_BASE_URL is missing or empty",
		},
		{
			name: "missing output directory",
			deps: func() federationDependencies {
				bad := newTestConfig(cfg.MisskeyBaseURL, filepath.Join(t.TempDir(), "missing", "report.md"))
				return successDependencies(bad, stubClient{}, now, func(string, string) error {
					t.Error("write must not run")
					return nil
				})
			},
			wantPhase: "preflight",
			wantErr:   "federation-report preflight failed",
		},
		{
			name: "meta fetch failure",
			deps: func() federationDependencies {
				return successDependencies(cfg, stubClient{metaErr: errors.New("boom")}, now, func(string, string) error {
					t.Error("write must not run")
					return nil
				})
			},
			wantPhase: "fetch",
			wantErr:   "federation-report fetch failed",
		},
		{
			name: "instances fetch failure",
			deps: func() federationDependencies {
				return successDependencies(cfg, stubClient{instancesErr: errors.New("boom")}, now, func(string, string) error {
					t.Error("write must not run")
					return nil
				})
			},
			wantPhase: "fetch",
			wantErr:   "federation-report fetch failed",
		},
		{
			name: "nil client",
			deps: func() federationDependencies {
				deps := successDependencies(cfg, nil, now, func(string, string) error {
					t.Error("write must not run")
					return nil
				})
				deps.newClient = func() Client { return nil }
				return deps
			},
			wantPhase: "fetch",
			wantErr:   "federation-report fetch failed",
		},
		{
			name: "write failure",
			deps: func() federationDependencies {
				return successDependencies(cfg, stubClient{}, now, func(string, string) error {
					return errors.New("disk full")
				})
			},
			wantPhase: "write",
			wantErr:   "federation-report write failed",
		},
		{
			name: "unexpected positional arguments",
			deps: func() federationDependencies {
				return successDependencies(cfg, stubClient{}, now, func(string, string) error {
					t.Error("write must not run")
					return nil
				})
			},
			wantPhase: "preflight",
			wantErr:   "federation-report preflight failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := []string(nil)
			if tt.name == "unexpected positional arguments" {
				args = []string{"surprise"}
			}
			var logOutput bytes.Buffer
			summary, err := runWithDependencies(context.Background(), args, tt.deps(), kinakolog.NewWithWriter(&logOutput))
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
			if summary.FailedPhase != tt.wantPhase {
				t.Fatalf("FailedPhase = %q, want %q", summary.FailedPhase, tt.wantPhase)
			}
			var entry map[string]any
			if decodeErr := json.Unmarshal(bytes.TrimSpace(logOutput.Bytes()), &entry); decodeErr != nil {
				t.Fatalf("decode log: %v", decodeErr)
			}
			if entry["result"] != "failure" || entry["failed_phase"] != tt.wantPhase {
				t.Errorf("log entry = %+v", entry)
			}
		})
	}
}

func TestRunWithDependenciesHelpAndCanceledContext(t *testing.T) {
	now := fixedNow()
	cfg := newTestConfig("https://misskey.example.com", filepath.Join(t.TempDir(), "report.md"))
	deps := successDependencies(cfg, stubClient{}, now, func(string, string) error {
		t.Error("write must not run")
		return nil
	})

	summary, err := runWithDependencies(context.Background(), []string{"--help"}, deps, nil)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("error = %v, want flag.ErrHelp", err)
	}
	if summary.FailedPhase != "" {
		t.Errorf("FailedPhase = %q", summary.FailedPhase)
	}
	for _, phase := range summary.Phases {
		if phase.Status != "skipped" {
			t.Errorf("phase %q status = %q", phase.Name, phase.Status)
		}
	}

	loadCalled := false
	deps.loadConfig = func() (*config.FederationReportConfig, error) {
		loadCalled = true
		return cfg, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	summary, err = runWithDependencies(ctx, nil, deps, kinakolog.NewWithWriter(io.Discard))
	if err == nil {
		t.Fatal("canceled run succeeded")
	}
	if summary.FailedPhase != "preflight" {
		t.Errorf("FailedPhase = %q, want preflight", summary.FailedPhase)
	}
	if loadCalled {
		t.Error("configuration loaded after cancellation")
	}
}

func TestWriteReportFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "report.md")
	content := "# report\n\nbody\n"

	if err := writeReportFile(path, content); err != nil {
		t.Fatalf("writeReportFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 644", info.Mode().Perm())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != content {
		t.Errorf("content = %q, want %q", got, content)
	}
	leftovers, err := filepath.Glob(filepath.Join(directory, ".federation-report-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}

	if err := writeReportFile(filepath.Join(directory, "missing", "report.md"), "x"); err == nil {
		t.Fatal("write into missing directory succeeded")
	}

	targetDirectory := filepath.Join(directory, "target")
	if err := os.Mkdir(targetDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeReportFile(targetDirectory, "x"); err == nil {
		t.Fatal("write onto a directory succeeded")
	}
	leftovers, err = filepath.Glob(filepath.Join(directory, ".federation-report-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind after failure: %v", leftovers)
	}
}

func TestLogFinalReportFields(t *testing.T) {
	summary := RunSummary{
		FailedPhase:             "write",
		OutputPath:              "/var/tmp/report.md",
		TotalInstances:          11,
		ActiveInstances:         3,
		NotRespondingInstances:  2,
		SuspendedInstances:      1,
		BlockedInstances:        4,
		NewInstancesCount:       5,
		UnhealthyInstancesCount: 2,
		UnhealthyWithFFCount:    1,
		Total:                   1500 * time.Millisecond,
		Phases: []PhaseSummary{
			{Name: "preflight", Status: "success", Duration: time.Second},
			{Name: "fetch", Status: "success", Duration: 400 * time.Millisecond},
			{Name: "write", Status: "failure", Duration: 100 * time.Millisecond},
		},
	}
	var buffer bytes.Buffer
	logFinalReport(context.Background(), kinakolog.NewWithWriter(&buffer), summary, errors.New("boom"))

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buffer.Bytes()), &entry); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	want := map[string]any{
		"msg":                       "federation-report final report",
		"result":                    "failure",
		"output_path":               "/var/tmp/report.md",
		"failed_phase":              "write",
		"total_instances":           float64(11),
		"active_instances":          float64(3),
		"not_responding_instances":  float64(2),
		"suspended_instances":       float64(1),
		"blocked_instances":         float64(4),
		"new_instances_count":       float64(5),
		"unhealthy_instances_count": float64(2),
		"unhealthy_with_ff_count":   float64(1),
		"total_duration_ms":         float64(1500),
	}
	for key, wantValue := range want {
		if entry[key] != wantValue {
			t.Errorf("%s = %v, want %v", key, entry[key], wantValue)
		}
	}
	phases, ok := entry["phases"].([]any)
	if !ok || len(phases) != 3 {
		t.Fatalf("phases = %v", entry["phases"])
	}
	firstPhase, ok := phases[0].(map[string]any)
	if !ok || firstPhase["name"] != "preflight" || firstPhase["status"] != "success" {
		t.Errorf("first phase = %v", phases[0])
	}
}

func TestRunWithSummaryHelp(t *testing.T) {
	summary, err := RunWithSummary(context.Background(), []string{"--help"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("error = %v, want flag.ErrHelp", err)
	}
	if summary.FailedPhase != "" {
		t.Fatalf("FailedPhase = %q, want empty", summary.FailedPhase)
	}
}

func TestRunWithSummaryLoadsEnvironmentAndWritesReport(t *testing.T) {
	now := time.Now().UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/meta":
			_, _ = io.WriteString(w, `{"version":"2026.1.0"}`)
		case "/api/federation/instances":
			if r.URL.Query().Get("offset") != "" {
				t.Errorf("unexpected query %q", r.URL.RawQuery)
			}
			var request instancesRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
			}
			if request.Offset != 0 {
				_ = json.NewEncoder(w).Encode([]InstanceRecord{})
				return
			}
			_ = json.NewEncoder(w).Encode([]InstanceRecord{{
				Host:                    "peer.example",
				SoftwareName:            new("mastodon"),
				SoftwareVersion:         new("4.2.0"),
				FirstRetrievedAt:        new(now.Add(-time.Hour)),
				LatestRequestReceivedAt: new(now.Add(-time.Hour)),
			}})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	outputPath := filepath.Join(t.TempDir(), "federation.md")
	t.Setenv("MISSKEY_BASE_URL", server.URL)
	t.Setenv("FEDERATION_REPORT_OUTPUT_PATH", outputPath)
	t.Setenv("MISSKEY_FEDERATION_REQUEST_TIMEOUT_SECONDS", "5")
	t.Setenv("MISSKEY_FEDERATION_NEW_WINDOW_HOURS", "24")
	t.Setenv("MISSKEY_FEDERATION_ACTIVE_WINDOW_DAYS", "3")

	summary, err := runWithSummary(context.Background(), nil, kinakolog.NewWithWriter(io.Discard))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if summary.TotalInstances != 1 || summary.ActiveInstances != 1 || summary.NewInstancesCount != 1 {
		t.Fatalf("summary = %+v", summary)
	}

	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 644", info.Mode().Perm())
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, want := range []string{
		"`24h`",
		"`3d`",
		"`2026.1.0`",
		"`peer.example`",
		"| 連合先総数 | 1 |",
	} {
		if !strings.Contains(string(content), want) {
			t.Errorf("report missing %q\n%s", want, content)
		}
	}
}
