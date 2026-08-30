package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// DBName is the fixed target database name. It is a constant (not an input)
// because the restore verification always targets the same database.
const DBName = "misskey"

// workloadNameRegexp matches a valid RFC 1123 label, which is the form
// Kubernetes workload names take.
var workloadNameRegexp = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Config holds the validated input contract for a single restore-test run.
//
// It is immutable by construction: there are no setters and LoadFromEnv is the
// only way to populate it. This enforces the "workload name is fixed" invariant
// — the runner must never derive or mutate these values.
//
// S3 credentials are intentionally NOT held here. The runner reads them from
// the standard AWS SDK credential chain (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY,
// AWS_SESSION_TOKEN) which the deploy-time manifest injects; Config only carries
// the endpoint/region/bucket/key that locate the fixed backup object.
type Config struct {
	// WebWorkload is the name of the web Kubernetes workload
	// (Deployment/StatefulSet) that the runner scales during the test.
	// It is fixed and never mutated.
	WebWorkload string

	// DBWorkload is the name of the database Kubernetes workload
	// (Deployment/StatefulSet) that the runner scales during the test.
	// It is fixed and never mutated.
	DBWorkload string

	// S3Endpoint is the endpoint used for the fixed backup object. Empty means
	// the AWS default endpoint; a non-empty value targets an S3-compatible
	// server and is accessed in path-style.
	S3Endpoint string
	// S3Region is the AWS region (or S3-compatible region) of the bucket.
	S3Region string
	// S3Bucket is the bucket holding the fixed backup object.
	S3Bucket string
	// S3Key is the fixed object key of the gzip dump (generation is never
	// chosen; only this exact key is fetched).
	S3Key string

	// DB holds the database connection inputs.
	DBHost string
	DBPort string
	DBUser string
	DBPass string
	// DBName is always DBName; it is not taken from the environment.
	DBName string
}

// requiredEnv maps each required input to its environment variable name.
var requiredEnv = []struct {
	key   string
	field string
}{
	{"WEB_WORKLOAD", "WEB_WORKLOAD"},
	{"DB_WORKLOAD", "DB_WORKLOAD"},
	{"S3_REGION", "S3_REGION"},
	{"S3_BUCKET", "S3_BUCKET"},
	{"S3_KEY", "S3_KEY"},
	{"DB_HOST", "DB_HOST"},
	{"DB_PORT", "DB_PORT"},
	{"DB_USER", "DB_USER"},
	{"DB_PASS", "DB_PASS"},
}

// LoadFromEnv reads and validates the input contract from the environment.
//
// It fails immediately (returning a non-nil error) when any required value is
// missing or empty. It performs no side effects — it never touches the
// workload, the database, or any external resource — which is what allows the
// caller to satisfy "validation before scale".
func LoadFromEnv() (*Config, error) {
	values := make(map[string]string, len(requiredEnv))
	for _, r := range requiredEnv {
		v := strings.TrimSpace(os.Getenv(r.key))
		if v == "" {
			return nil, fmt.Errorf("required input %s is missing or empty", r.key)
		}
		values[r.key] = v
	}

	if !workloadNameRegexp.MatchString(values["WEB_WORKLOAD"]) {
		return nil, fmt.Errorf("WEB_WORKLOAD %q is not a valid RFC 1123 label", values["WEB_WORKLOAD"])
	}
	if !workloadNameRegexp.MatchString(values["DB_WORKLOAD"]) {
		return nil, fmt.Errorf("DB_WORKLOAD %q is not a valid RFC 1123 label", values["DB_WORKLOAD"])
	}

	// S3_ENDPOINT is optional: empty selects the AWS default endpoint.
	s3Endpoint := strings.TrimSpace(os.Getenv("S3_ENDPOINT"))
	if err := validateS3Endpoint(s3Endpoint); err != nil {
		return nil, err
	}

	cfg := &Config{
		WebWorkload: values["WEB_WORKLOAD"],
		DBWorkload:  values["DB_WORKLOAD"],
		S3Endpoint:  s3Endpoint,
		S3Region:    values["S3_REGION"],
		S3Bucket:    values["S3_BUCKET"],
		S3Key:       values["S3_KEY"],
		DBHost:      values["DB_HOST"],
		DBPort:      values["DB_PORT"],
		DBUser:      values["DB_USER"],
		DBPass:      values["DB_PASS"],
		DBName:      DBName,
	}
	return cfg, nil
}

// validateS3Endpoint rejects a clearly malformed endpoint. The empty string is
// allowed and selects the AWS default endpoint.
func validateS3Endpoint(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("S3_ENDPOINT %q is not a valid URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("S3_ENDPOINT %q must use http or https scheme", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("S3_ENDPOINT %q must include a host", raw)
	}
	return nil
}

// Loggable returns the config as a map suitable for structured logging.
// It omits the secret database password.
func (c *Config) Loggable() map[string]any {
	return map[string]any{
		"web_workload": c.WebWorkload,
		"db_workload":  c.DBWorkload,
		"s3_endpoint":  c.S3Endpoint,
		"s3_region":    c.S3Region,
		"s3_bucket":    c.S3Bucket,
		"s3_key":       c.S3Key,
		"db_host":      c.DBHost,
		"db_port":      c.DBPort,
		"db_user":      c.DBUser,
		"db_name":      c.DBName,
	}
}
