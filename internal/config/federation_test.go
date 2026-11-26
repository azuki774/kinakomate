package config

import (
	"strings"
	"testing"
	"time"
)

// federationEnv returns every federation-report variable so tests never
// inherit optional values from the invoker's shell.
func federationEnv() map[string]string {
	return map[string]string{
		"MISSKEY_BASE_URL":                           "https://misskey.example",
		"FEDERATION_REPORT_OUTPUT_PATH":              "/tmp/federation-report.md",
		"MISSKEY_FEDERATION_REQUEST_TIMEOUT_SECONDS": "",
		"MISSKEY_FEDERATION_NEW_WINDOW_HOURS":        "",
		"MISSKEY_FEDERATION_ACTIVE_WINDOW_DAYS":      "",
	}
}

func TestLoadFederationReportFromEnv_Defaults(t *testing.T) {
	setEnv(t, federationEnv())

	cfg, err := LoadFederationReportFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MisskeyBaseURL != "https://misskey.example" {
		t.Errorf("MisskeyBaseURL = %q, want https://misskey.example", cfg.MisskeyBaseURL)
	}
	if cfg.OutputPath != "/tmp/federation-report.md" {
		t.Errorf("OutputPath = %q, want /tmp/federation-report.md", cfg.OutputPath)
	}
	if cfg.RequestTimeout != 10*time.Second {
		t.Errorf("RequestTimeout = %v, want %v", cfg.RequestTimeout, DefaultFederationRequestTimeout)
	}
	if cfg.NewWindowHours != 168 {
		t.Errorf("NewWindowHours = %d, want 168", cfg.NewWindowHours)
	}
	if cfg.NewWindow != 168*time.Hour {
		t.Errorf("NewWindow = %v, want %v", cfg.NewWindow, DefaultFederationNewWindow)
	}
	if cfg.ActiveWindowDays != 7 {
		t.Errorf("ActiveWindowDays = %d, want 7", cfg.ActiveWindowDays)
	}
	if cfg.ActiveWindow != 7*24*time.Hour {
		t.Errorf("ActiveWindow = %v, want %v", cfg.ActiveWindow, DefaultFederationActiveWindow)
	}
}

func TestLoadFederationReportFromEnv_CustomValues(t *testing.T) {
	env := federationEnv()
	env["MISSKEY_BASE_URL"] = "  https://misskey.example/  "
	env["FEDERATION_REPORT_OUTPUT_PATH"] = "  /var/lib/kinakomate/report.md  "
	env["MISSKEY_FEDERATION_REQUEST_TIMEOUT_SECONDS"] = "25"
	env["MISSKEY_FEDERATION_NEW_WINDOW_HOURS"] = " 24 "
	env["MISSKEY_FEDERATION_ACTIVE_WINDOW_DAYS"] = "3"
	setEnv(t, env)

	cfg, err := LoadFederationReportFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MisskeyBaseURL != "https://misskey.example/" {
		t.Errorf("MisskeyBaseURL = %q, want trimmed value", cfg.MisskeyBaseURL)
	}
	if cfg.OutputPath != "/var/lib/kinakomate/report.md" {
		t.Errorf("OutputPath = %q, want trimmed value", cfg.OutputPath)
	}
	if cfg.RequestTimeout != 25*time.Second {
		t.Errorf("RequestTimeout = %v, want 25s", cfg.RequestTimeout)
	}
	if cfg.NewWindowHours != 24 || cfg.NewWindow != 24*time.Hour {
		t.Errorf("new window = (%d, %v), want (24, 24h)", cfg.NewWindowHours, cfg.NewWindow)
	}
	if cfg.ActiveWindowDays != 3 || cfg.ActiveWindow != 3*24*time.Hour {
		t.Errorf("active window = (%d, %v), want (3, 72h)", cfg.ActiveWindowDays, cfg.ActiveWindow)
	}
}

func TestLoadFederationReportFromEnv_WindowBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		hours     string
		days      string
		wantHours int
		wantDays  int
	}{
		{name: "minimum", hours: "1", days: "1", wantHours: 1, wantDays: 1},
		{name: "maximum", hours: "8760", days: "365", wantHours: 8760, wantDays: 365},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := federationEnv()
			env["MISSKEY_FEDERATION_NEW_WINDOW_HOURS"] = tc.hours
			env["MISSKEY_FEDERATION_ACTIVE_WINDOW_DAYS"] = tc.days
			setEnv(t, env)

			cfg, err := LoadFederationReportFromEnv()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.NewWindowHours != tc.wantHours {
				t.Errorf("NewWindowHours = %d, want %d", cfg.NewWindowHours, tc.wantHours)
			}
			if cfg.NewWindow != time.Duration(tc.wantHours)*time.Hour {
				t.Errorf("NewWindow = %v, want %v", cfg.NewWindow, time.Duration(tc.wantHours)*time.Hour)
			}
			if cfg.ActiveWindowDays != tc.wantDays {
				t.Errorf("ActiveWindowDays = %d, want %d", cfg.ActiveWindowDays, tc.wantDays)
			}
			if cfg.ActiveWindow != time.Duration(tc.wantDays)*24*time.Hour {
				t.Errorf("ActiveWindow = %v, want %v", cfg.ActiveWindow, time.Duration(tc.wantDays)*24*time.Hour)
			}
		})
	}
}

func TestLoadFederationReportFromEnv_WhitespaceOptionalUsesDefaults(t *testing.T) {
	env := federationEnv()
	env["MISSKEY_FEDERATION_REQUEST_TIMEOUT_SECONDS"] = "   "
	env["MISSKEY_FEDERATION_NEW_WINDOW_HOURS"] = "  "
	env["MISSKEY_FEDERATION_ACTIVE_WINDOW_DAYS"] = "\t"
	setEnv(t, env)

	cfg, err := LoadFederationReportFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RequestTimeout != DefaultFederationRequestTimeout ||
		cfg.NewWindowHours != DefaultFederationNewWindowHours ||
		cfg.ActiveWindowDays != DefaultFederationActiveWindowDays {
		t.Fatalf("whitespace-only optional values did not use defaults: %+v", cfg)
	}
}

func TestLoadFederationReportFromEnv_MissingRequired(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		value string
	}{
		{name: "base URL empty", key: "MISSKEY_BASE_URL", value: ""},
		{name: "base URL whitespace", key: "MISSKEY_BASE_URL", value: "   "},
		{name: "output path empty", key: "FEDERATION_REPORT_OUTPUT_PATH", value: ""},
		{name: "output path whitespace", key: "FEDERATION_REPORT_OUTPUT_PATH", value: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := federationEnv()
			env[tc.key] = tc.value
			setEnv(t, env)

			_, err := LoadFederationReportFromEnv()
			if err == nil {
				t.Fatalf("expected error when %s is %q", tc.key, tc.value)
			}
			want := "required input " + tc.key + " is missing or empty"
			if err.Error() != want {
				t.Fatalf("error = %q, want %q", err, want)
			}
		})
	}
}

func TestLoadFederationReportFromEnv_IgnoresOtherCommandsEnv(t *testing.T) {
	env := federationEnv()
	// These are missing, malformed, or rejected by the other loaders on
	// purpose: federation-report must not read them.
	env["WEB_WORKLOAD"] = "Not_A_Workload"
	env["DB_WORKLOAD"] = ""
	env["DB_HOST"] = ""
	env["DB_PORT"] = "not-a-port"
	env["DB_USER"] = ""
	env["DB_PASS"] = ""
	env["DB_ANALYZE_TIMEOUT_SECONDS"] = "0"
	env["S3_REGION"] = ""
	env["S3_BUCKET"] = ""
	env["S3_KEY"] = ""
	env["S3_ENDPOINT"] = "ftp://invalid.example"
	setEnv(t, env)

	cfg, err := LoadFederationReportFromEnv()
	if err != nil {
		t.Fatalf("unexpected error from unrelated environment: %v", err)
	}
	if cfg.MisskeyBaseURL != "https://misskey.example" || cfg.OutputPath != "/tmp/federation-report.md" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.RequestTimeout != DefaultFederationRequestTimeout ||
		cfg.NewWindowHours != DefaultFederationNewWindowHours ||
		cfg.ActiveWindowDays != DefaultFederationActiveWindowDays {
		t.Fatalf("defaults were not applied: %+v", cfg)
	}
}

func TestLoadFederationReportFromEnv_InvalidBaseURL(t *testing.T) {
	for _, tc := range []struct {
		name      string
		url       string
		sensitive string
	}{
		{name: "invalid scheme", url: "ftp://misskey.example"},
		{name: "missing host", url: "https://"},
		{name: "non-root path", url: "https://misskey.example/api"},
		{name: "userinfo", url: "https://user:base-url-secret@misskey.example", sensitive: "base-url-secret"},
		{name: "query", url: "https://misskey.example/?token=query-secret", sensitive: "query-secret"},
		{name: "fragment", url: "https://misskey.example/#fragment-secret", sensitive: "fragment-secret"},
		{name: "malformed URL", url: "https://misskey.example/%zz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := federationEnv()
			env["MISSKEY_BASE_URL"] = tc.url
			setEnv(t, env)

			_, err := LoadFederationReportFromEnv()
			if err == nil {
				t.Fatalf("expected error for MISSKEY_BASE_URL %q", tc.url)
			}
			if !strings.Contains(err.Error(), "MISSKEY_BASE_URL") {
				t.Errorf("error %q does not identify MISSKEY_BASE_URL", err)
			}
			if tc.sensitive != "" && strings.Contains(err.Error(), tc.sensitive) {
				t.Errorf("error %q leaks sensitive value %q", err, tc.sensitive)
			}
		})
	}
}

func TestLoadFederationReportFromEnv_InvalidRequestTimeout(t *testing.T) {
	const positive = "MISSKEY_FEDERATION_REQUEST_TIMEOUT_SECONDS must be a positive integer number of seconds"
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "zero", value: "0", want: positive},
		{name: "negative", value: "-1", want: positive},
		{name: "decimal", value: "1.5", want: positive},
		{name: "non-numeric", value: "timeout-secret", want: positive},
		{name: "integer overflow", value: "9223372036854775808", want: positive},
		{name: "duration overflow", value: "9223372037", want: "MISSKEY_FEDERATION_REQUEST_TIMEOUT_SECONDS is too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := federationEnv()
			env["MISSKEY_FEDERATION_REQUEST_TIMEOUT_SECONDS"] = tc.value
			setEnv(t, env)

			_, err := LoadFederationReportFromEnv()
			if err == nil {
				t.Fatalf("expected error for MISSKEY_FEDERATION_REQUEST_TIMEOUT_SECONDS=%q", tc.value)
			}
			if err.Error() != tc.want {
				t.Fatalf("error = %q, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), tc.value) {
				t.Errorf("error %q leaks rejected value %q", err, tc.value)
			}
		})
	}
}

func TestLoadFederationReportFromEnv_InvalidNewWindowHours(t *testing.T) {
	const want = "MISSKEY_FEDERATION_NEW_WINDOW_HOURS must be an integer from 1 through 8760"
	for _, value := range []string{"0", "-1", "8761", "1.5", "hours-secret", "99999999999999999999"} {
		t.Run(value, func(t *testing.T) {
			env := federationEnv()
			env["MISSKEY_FEDERATION_NEW_WINDOW_HOURS"] = value
			setEnv(t, env)

			_, err := LoadFederationReportFromEnv()
			if err == nil {
				t.Fatalf("expected error for MISSKEY_FEDERATION_NEW_WINDOW_HOURS=%q", value)
			}
			if err.Error() != want {
				t.Fatalf("error = %q, want %q", err, want)
			}
		})
	}
}

func TestLoadFederationReportFromEnv_InvalidActiveWindowDays(t *testing.T) {
	const want = "MISSKEY_FEDERATION_ACTIVE_WINDOW_DAYS must be an integer from 1 through 365"
	for _, value := range []string{"0", "-1", "366", "1.5", "days-secret", "99999999999999999999"} {
		t.Run(value, func(t *testing.T) {
			env := federationEnv()
			env["MISSKEY_FEDERATION_ACTIVE_WINDOW_DAYS"] = value
			setEnv(t, env)

			_, err := LoadFederationReportFromEnv()
			if err == nil {
				t.Fatalf("expected error for MISSKEY_FEDERATION_ACTIVE_WINDOW_DAYS=%q", value)
			}
			if err.Error() != want {
				t.Fatalf("error = %q, want %q", err, want)
			}
		})
	}
}
