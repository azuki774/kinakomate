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
		"WORKLOAD": "misskey",
		"S3_URI":   "https://s3.example.com/backups/misskey",
		"DB_HOST":  "db",
		"DB_PORT":  "5432",
		"DB_USER":  "misskey",
		"DB_PASS":  "secret",
	}
}

func TestLoadFromEnv_MissingRequired(t *testing.T) {
	for _, missing := range []string{"WORKLOAD", "S3_URI", "DB_HOST", "DB_PORT", "DB_USER", "DB_PASS"} {
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
	env["WORKLOAD"] = "   "
	setEnv(t, env)

	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("expected error when WORKLOAD is whitespace only")
	}
}

func TestLoadFromEnv_InvalidWorkload(t *testing.T) {
	env := validEnv()
	env["WORKLOAD"] = "Misskey_App"
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
	if cfg.Workload != "misskey" {
		t.Errorf("Workload = %q, want misskey", cfg.Workload)
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
		Workload: "misskey",
		S3Bucket: "b",
		DBPass:   "secret",
		DBName:   DBName,
	}
	loggable := cfg.Loggable()
	if _, ok := loggable["db_pass"]; ok {
		t.Error("Loggable must not include db_pass")
	}
	if loggable["workload"] != "misskey" {
		t.Error("Loggable should include workload")
	}
}
