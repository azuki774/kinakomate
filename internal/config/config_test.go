package config

import (
	"strings"
	"testing"
)

func setEnv(t *testing.T, env map[string]string) {
	t.Helper()
	t.Cleanup(func() {
		for k := range env {
			t.Setenv(k, "")
		}
	})
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"WEB_WORKLOAD":     "misskey-web",
		"DB_WORKLOAD":      "misskey-db-v18",
		"S3_REGION":        "us-east-1",
		"S3_BUCKET":        "backups",
		"S3_KEY":           "misskey/daily/dump.sql.gz",
		"MISSKEY_BASE_URL": "https://misskey.example",
		"DB_HOST":          "db",
		"DB_PORT":          "5432",
		"DB_USER":          "misskey",
		"DB_PASS":          "secret",
	}
}

func TestLoadFromEnv_MissingRequired(t *testing.T) {
	for _, missing := range []string{
		"WEB_WORKLOAD", "DB_WORKLOAD", "S3_REGION", "S3_BUCKET", "S3_KEY",
		"MISSKEY_BASE_URL", "DB_HOST", "DB_PORT", "DB_USER", "DB_PASS",
	} {
		t.Run(missing, func(t *testing.T) {
			env := validEnv()
			delete(env, missing)
			setEnv(t, env)

			if _, err := LoadFromEnv(); err == nil {
				t.Fatalf("expected error when %s is missing", missing)
			}
		})
	}
}

func TestLoadFromEnv_EmptyRequired(t *testing.T) {
	env := validEnv()
	env["WEB_WORKLOAD"] = "   "
	setEnv(t, env)

	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("expected error when WEB_WORKLOAD is whitespace only")
	}
}

func TestLoadFromEnv_InvalidWorkload(t *testing.T) {
	env := validEnv()
	env["DB_WORKLOAD"] = "Misskey_App"
	setEnv(t, env)

	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("expected error for invalid workload name")
	}
}

func TestLoadFromEnv_InvalidEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint string
	}{
		{name: "no scheme", endpoint: "s3.example.com"},
		{name: "bad scheme", endpoint: "ftp://s3.example.com"},
		{name: "no host", endpoint: "https://"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env["S3_ENDPOINT"] = tc.endpoint
			setEnv(t, env)

			if _, err := LoadFromEnv(); err == nil {
				t.Fatalf("expected error for S3_ENDPOINT %q", tc.endpoint)
			}
		})
	}
}

func TestLoadFromEnv_MisskeyBaseURLValid(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want string
	}{
		{name: "https without path", url: "https://misskey.example", want: "https://misskey.example"},
		{name: "http with root path", url: "http://misskey.example/", want: "http://misskey.example/"},
		{name: "trimmed", url: "  https://misskey.example/  ", want: "https://misskey.example/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env["MISSKEY_BASE_URL"] = tc.url
			setEnv(t, env)

			cfg, err := LoadFromEnv()
			if err != nil {
				t.Fatalf("unexpected error for MISSKEY_BASE_URL %q: %v", tc.url, err)
			}
			if cfg.MisskeyBaseURL != tc.want {
				t.Errorf("MisskeyBaseURL = %q, want %q", cfg.MisskeyBaseURL, tc.want)
			}
		})
	}
}

func TestLoadFromEnv_InvalidMisskeyBaseURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "invalid scheme", url: "ftp://misskey.example"},
		{name: "uppercase scheme", url: "HTTP://misskey.example"},
		{name: "mixed-case scheme", url: "HtTp://misskey.example"},
		{name: "missing host", url: "https://"},
		{name: "non-root path", url: "https://misskey.example/api"},
		{name: "query", url: "https://misskey.example/?foo=bar"},
		{name: "fragment", url: "https://misskey.example/#section"},
		{name: "userinfo", url: "https://user:pass@misskey.example"},
		{name: "empty query", url: "https://misskey.example?"},
		{name: "empty fragment", url: "https://misskey.example#"},
		{name: "whitespace only", url: "   "},
		{name: "malformed URL", url: "https://misskey.example/%zz"},
		{name: "hostless URL with port", url: "https://:8448"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env["MISSKEY_BASE_URL"] = tc.url
			setEnv(t, env)

			_, err := LoadFromEnv()
			if err == nil {
				t.Fatalf("expected error for MISSKEY_BASE_URL %q", tc.url)
			}
			if !strings.Contains(err.Error(), "MISSKEY_BASE_URL") {
				t.Errorf("error %q does not identify MISSKEY_BASE_URL", err)
			}
		})
	}
}

func TestLoadFromEnv_MisskeyBaseURLErrorDoesNotLeakInput(t *testing.T) {
	for _, tc := range []struct {
		name      string
		url       string
		sensitive string
	}{
		{name: "userinfo", url: "https://user:super-secret@misskey.example", sensitive: "super-secret"},
		{name: "query", url: "https://misskey.example/?token=query-secret", sensitive: "query-secret"},
		{name: "malformed URL", url: "https://malformed-secret.example/%zz", sensitive: "malformed-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env["MISSKEY_BASE_URL"] = tc.url
			setEnv(t, env)

			_, err := LoadFromEnv()
			if err == nil {
				t.Fatalf("expected error for MISSKEY_BASE_URL %q", tc.url)
			}
			if strings.Contains(err.Error(), tc.url) {
				t.Errorf("error %q leaks rejected URL", err)
			}
			if strings.Contains(err.Error(), tc.sensitive) {
				t.Errorf("error %q leaks sensitive value %q", err, tc.sensitive)
			}
		})
	}
}

func TestLoadFromEnv_EmptyEndpointAllowed(t *testing.T) {
	env := validEnv()
	env["S3_ENDPOINT"] = ""
	setEnv(t, env)

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.S3Endpoint != "" {
		t.Errorf("S3Endpoint = %q, want empty (AWS default)", cfg.S3Endpoint)
	}
}

func TestLoadFromEnv_Success(t *testing.T) {
	env := validEnv()
	setEnv(t, env)

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.WebWorkload != "misskey-web" {
		t.Errorf("WebWorkload = %q, want misskey-web", cfg.WebWorkload)
	}
	if cfg.DBWorkload != "misskey-db-v18" {
		t.Errorf("DBWorkload = %q, want misskey-db-v18", cfg.DBWorkload)
	}
	if cfg.DBName != DBName {
		t.Errorf("DBName = %q, want %q", cfg.DBName, DBName)
	}
	if cfg.S3Bucket != "backups" || cfg.S3Key != "misskey/daily/dump.sql.gz" {
		t.Errorf("S3 = (%q,%q), want (backups, misskey/daily/dump.sql.gz)",
			cfg.S3Bucket, cfg.S3Key)
	}
}

func TestConfig_Loggable_OmitsPassword(t *testing.T) {
	cfg := &Config{
		WebWorkload:    "misskey-web",
		DBWorkload:     "misskey-db-v18",
		MisskeyBaseURL: "https://misskey.example/",
		S3Bucket:       "b",
		DBPass:         "secret",
		DBName:         DBName,
	}
	loggable := cfg.Loggable()
	if _, ok := loggable["db_pass"]; ok {
		t.Error("Loggable must not include db_pass")
	}
	if loggable["web_workload"] != "misskey-web" {
		t.Error("Loggable should include web_workload")
	}
	if loggable["db_workload"] != "misskey-db-v18" {
		t.Error("Loggable should include db_workload")
	}
	if loggable["misskey_base_url"] != "https://misskey.example/" {
		t.Error("Loggable should include misskey_base_url")
	}
}
