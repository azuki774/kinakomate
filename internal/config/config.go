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
type Config struct {
	// WebWorkload is the name of the web Kubernetes workload
	// (Deployment/StatefulSet) that the runner scales during the test.
	// It is fixed and never mutated.
	WebWorkload string

	// DBWorkload is the name of the database Kubernetes workload
	// (Deployment/StatefulSet) that the runner scales during the test.
	// It is fixed and never mutated.
	DBWorkload string

	// S3 holds the parsed result of S3_URI.
	S3Bucket   string
	S3Prefix   string
	S3Endpoint string

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
	{"S3_URI", "S3_URI"},
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

	bucket, prefix, endpoint, err := parseS3URI(values["S3_URI"])
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		WebWorkload: values["WEB_WORKLOAD"],
		DBWorkload:  values["DB_WORKLOAD"],
		S3Bucket:    bucket,
		S3Prefix:    prefix,
		S3Endpoint:  endpoint,
		DBHost:      values["DB_HOST"],
		DBPort:      values["DB_PORT"],
		DBUser:      values["DB_USER"],
		DBPass:      values["DB_PASS"],
		DBName:      DBName,
	}
	return cfg, nil
}

// parseS3URI parses an S3 location into its bucket, prefix, and endpoint.
//
// Supported forms:
//   - s3://bucket/prefix            (AWS; endpoint resolved by the SDK later)
//   - https://endpoint/bucket/prefix (S3-compatible; endpoint is explicit)
//   - http://endpoint/bucket/prefix
func parseS3URI(raw string) (bucket, prefix, endpoint string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", "", fmt.Errorf("S3_URI %q is not a valid URI: %w", raw, err)
	}

	switch u.Scheme {
	case "s3":
		endpoint = ""
		bucket = u.Host
		prefix = strings.TrimPrefix(u.Path, "/")
	case "http", "https":
		endpoint = u.Scheme + "://" + u.Host
		path := strings.TrimPrefix(u.Path, "/")
		parts := strings.SplitN(path, "/", 2)
		bucket = parts[0]
		if len(parts) == 2 {
			prefix = strings.TrimPrefix(parts[1], "/")
		}
	default:
		return "", "", "", fmt.Errorf("S3_URI %q has unsupported scheme %q (want s3, http, or https)", raw, u.Scheme)
	}

	if bucket == "" {
		return "", "", "", fmt.Errorf("S3_URI %q does not contain a bucket", raw)
	}
	return bucket, prefix, endpoint, nil
}

// Loggable returns the config as a map suitable for structured logging.
// It omits the secret database password.
func (c *Config) Loggable() map[string]any {
	return map[string]any{
		"web_workload": c.WebWorkload,
		"db_workload":  c.DBWorkload,
		"s3_bucket":    c.S3Bucket,
		"s3_prefix":    c.S3Prefix,
		"s3_endpoint":  c.S3Endpoint,
		"db_host":      c.DBHost,
		"db_port":      c.DBPort,
		"db_user":      c.DBUser,
		"db_name":      c.DBName,
	}
}
