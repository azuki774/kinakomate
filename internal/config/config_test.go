package config

import (
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
		"WEB_WORKLOAD": "misskey-web",
		"DB_WORKLOAD":  "misskey-db-v18",
		"S3_URI":       "https://s3.example.com/backups/misskey",
		"DB_HOST":      "db",
		"DB_PORT":      "5432",
		"DB_USER":      "misskey",
		"DB_PASS":      "secret",
	}
}

func TestLoadFromEnv_MissingRequired(t *testing.T) {
	for _, missing := range []string{"WEB_WORKLOAD", "DB_WORKLOAD", "S3_URI", "DB_HOST", "DB_PORT", "DB_USER", "DB_PASS"} {
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
}

func TestParseS3URI(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		bucket   string
		prefix   string
		endpoint string
		wantErr  bool
	}{
		{
			name:     "s3 scheme with prefix",
			raw:      "s3://mybucket/misskey/daily",
			bucket:   "mybucket",
			prefix:   "misskey/daily",
			endpoint: "",
		},
		{
			name:     "s3 scheme no prefix",
			raw:      "s3://mybucket",
			bucket:   "mybucket",
			prefix:   "",
			endpoint: "",
		},
		{
			name:     "https compatible with prefix",
			raw:      "https://s3.example.com/backups/misskey",
			bucket:   "backups",
			prefix:   "misskey",
			endpoint: "https://s3.example.com",
		},
		{
			name:    "no scheme errors",
			raw:     "mybucket/misskey",
			wantErr: true,
		},
		{
			name:    "no bucket errors",
			raw:     "s3://",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bucket, prefix, endpoint, err := parseS3URI(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if bucket != tc.bucket || prefix != tc.prefix || endpoint != tc.endpoint {
				t.Errorf("parseS3URI(%q) = (%q,%q,%q), want (%q,%q,%q)",
					tc.raw, bucket, prefix, endpoint, tc.bucket, tc.prefix, tc.endpoint)
			}
		})
	}
}

func TestConfig_Loggable_OmitsPassword(t *testing.T) {
	cfg := &Config{
		WebWorkload: "misskey-web",
		DBWorkload:  "misskey-db-v18",
		S3Bucket:    "b",
		DBPass:      "secret",
		DBName:      DBName,
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
}
