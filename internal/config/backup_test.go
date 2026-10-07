package config

import (
	"strings"
	"testing"
)

func setBackupEnv(t *testing.T) {
	t.Helper()
	for name, value := range map[string]string{
		"DB_HOST": "db", "DB_PORT": "5432", "DB_USER": "backup",
		"DB_PASS": "secret", "DB_NAME": "app", "S3_REGION": "us-east-1",
		"S3_BUCKET": "backups", "S3_KEY": "app/latest.sql.gz", "S3_ENDPOINT": "",
		"WEB_WORKLOAD": "", "DB_WORKLOAD": "", "MISSKEY_BASE_URL": "",
		"DB_ANALYZE_TIMEOUT_SECONDS": "", "MISSKEY_GTL_REQUEST_TIMEOUT_SECONDS": "",
		"MISSKEY_GTL_RETRY_INTERVAL_SECONDS": "", "MISSKEY_GTL_RETRY_TIMEOUT_SECONDS": "",
	} {
		t.Setenv(name, value)
	}
}

func TestLoadBackupFromEnvUsesOnlyBackupInputs(t *testing.T) {
	setBackupEnv(t)
	t.Setenv("DB_PASS", "  pass\t ")
	t.Setenv("DB_NAME", "host=database db")
	t.Setenv("S3_KEY", " /literal/../key.sql.gz ")

	cfg, err := LoadBackupFromEnv()
	if err != nil {
		t.Fatalf("LoadBackupFromEnv() error = %v", err)
	}
	if cfg.DBPass != "  pass\t " {
		t.Errorf("DBPass = %q, want exact environment value", cfg.DBPass)
	}
	if cfg.DBName != "host=database db" {
		t.Errorf("DBName = %q, want exact environment value", cfg.DBName)
	}
	if cfg.S3Key != " /literal/../key.sql.gz " {
		t.Errorf("S3Key = %q, want exact environment value", cfg.S3Key)
	}
}

func TestLoadBackupFromEnvRequiresBackupInputsOnly(t *testing.T) {
	for _, missing := range []string{
		"DB_HOST", "DB_PORT", "DB_USER", "DB_PASS", "DB_NAME",
		"S3_REGION", "S3_BUCKET", "S3_KEY",
	} {
		t.Run(missing, func(t *testing.T) {
			setBackupEnv(t)
			t.Setenv(missing, "")
			if _, err := LoadBackupFromEnv(); err == nil {
				t.Fatalf("LoadBackupFromEnv() succeeded with empty %s", missing)
			}
		})
	}
}

func TestLoadBackupFromEnvRejectsInvalidPortWithoutLeakingValue(t *testing.T) {
	setBackupEnv(t)
	t.Setenv("DB_PORT", "db-port-secret")

	_, err := LoadBackupFromEnv()
	if err == nil {
		t.Fatal("LoadBackupFromEnv() succeeded with an invalid port")
	}
	if strings.Contains(err.Error(), "db-port-secret") {
		t.Fatalf("error leaked invalid value: %v", err)
	}
}

func TestLoadBackupFromEnvRejectsInvalidEndpointWithoutLeakingValue(t *testing.T) {
	setBackupEnv(t)
	t.Setenv("S3_ENDPOINT", "https://endpoint-secret/%zz")

	_, err := LoadBackupFromEnv()
	if err == nil {
		t.Fatal("LoadBackupFromEnv() succeeded with an invalid endpoint")
	}
	if strings.Contains(err.Error(), "endpoint-secret") {
		t.Fatalf("error leaked endpoint: %v", err)
	}
}
