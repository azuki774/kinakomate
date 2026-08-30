package restore

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/azuki774/kinakomate/internal/config"
)

// s3API is the subset of the AWS S3 client the runner uses. It lets tests
// inject a fake; *s3.Client satisfies it.
type s3API interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// objectStorage fetches the fixed gzip backup object from S3.
//
// The runner only uses the read-only credentials injected as standard AWS SDK
// environment variables (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY,
// AWS_SESSION_TOKEN) via the default credential chain; it never reads a secret
// from the Kubernetes API. The endpoint/region/bucket/key come from Config.
type objectStorage struct {
	client s3API
	logger *slog.Logger
}

// newObjectStorage builds an S3 client from the config and the AWS SDK default
// credential chain. An empty S3Endpoint selects the AWS default endpoint;
// a non-empty endpoint is an S3-compatible server accessed in path-style.
func newObjectStorage(ctx context.Context, cfg *config.Config) (*objectStorage, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{}
	if cfg.S3Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(cfg.S3Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.S3Endpoint != ""
		if cfg.S3Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.S3Endpoint)
		}
	})

	return &objectStorage{
		client: client,
		logger: slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}, nil
}

// newObjectStorageWithClient builds an objectStorage over a provided s3API. It
// is used by tests to inject a fake client.
func newObjectStorageWithClient(client s3API) *objectStorage {
	return &objectStorage{
		client: client,
		logger: slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}
}

// CheckConnection verifies the fixed object is reachable and readable via a
// HEAD request. It does not download the body.
func (o *objectStorage) CheckConnection(ctx context.Context, cfg *config.Config) error {
	out, err := o.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(cfg.S3Bucket),
		Key:    aws.String(cfg.S3Key),
	})
	if err != nil {
		return fmt.Errorf("S3 HEAD %s/%s: %w", cfg.S3Bucket, cfg.S3Key, err)
	}

	o.logger.InfoContext(ctx, "s3 connection check ok",
		"s3_bucket", cfg.S3Bucket,
		"s3_key", cfg.S3Key,
		"s3_etag", aws.ToString(out.ETag),
		"s3_size", aws.ToInt64(out.ContentLength),
	)
	return nil
}

// DownloadAndExtract fetches the fixed object, streams it to a temporary gzip
// file, and validates that the gzip stream is complete and not truncated. The
// plaintext SQL is never expanded to disk. On success the caller owns the
// returned dump and must call Dump.Cleanup.
func (o *objectStorage) DownloadAndExtract(ctx context.Context, cfg *config.Config) (*Dump, error) {
	out, err := o.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(cfg.S3Bucket),
		Key:    aws.String(cfg.S3Key),
	})
	if err != nil {
		return nil, fmt.Errorf("S3 GET %s/%s: %w", cfg.S3Bucket, cfg.S3Key, err)
	}
	defer out.Body.Close() //nolint:errcheck

	tmp, err := os.CreateTemp("", "kinakomate-dump-*.sql.gz")
	if err != nil {
		return nil, fmt.Errorf("create temp dump file: %w", err)
	}
	tmpPath := tmp.Name()
	// If copying or validating fails, clean up the partial file. On success the
	// caller owns the file and removes it via Dump.Cleanup.
	defer func() {
		tmp.Close() //nolint:errcheck
	}()

	if err := tmp.Chmod(0o600); err != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return nil, fmt.Errorf("chmod temp dump file: %w", err)
	}

	if _, err := io.Copy(tmp, out.Body); err != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return nil, fmt.Errorf("stream S3 object to temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return nil, fmt.Errorf("close temp dump file: %w", err)
	}

	if err := validateGzipStream(tmpPath); err != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return nil, err
	}

	o.logger.InfoContext(ctx, "s3 dump fetched and validated",
		"s3_bucket", cfg.S3Bucket,
		"s3_key", cfg.S3Key,
		"s3_etag", aws.ToString(out.ETag),
		"s3_size", out.ContentLength,
		"s3_stored_size", tmpStatSize(tmpPath),
	)

	return &Dump{
		Path:   tmpPath,
		Bucket: cfg.S3Bucket,
		Key:    cfg.S3Key,
		ETag:   aws.ToString(out.ETag),
		Size:   aws.ToInt64(out.ContentLength),
	}, nil
}

// tmpStatSize returns the on-disk size of the staged gzip file, or 0 if it
// cannot be determined. This is a best-effort value for the structured log.
func tmpStatSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// validateGzipStream reads the whole gzip stream from disk and discards it,
// forcing gzip to verify the CRC checksum and detect a truncated stream. On
// success the file is a complete gzip archive.
func validateGzipStream(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open temp dump for validation: %w", err)
	}
	defer f.Close() //nolint:errcheck

	zr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("invalid gzip stream: %w", err)
	}
	defer zr.Close() //nolint:errcheck

	if _, err := io.Copy(io.Discard, zr); err != nil {
		return fmt.Errorf("gzip stream truncated or corrupt: %w", err)
	}
	// gzip.NewReader reports ErrChecksum on a bad CRC.
	if err := zr.Close(); err != nil {
		return fmt.Errorf("gzip checksum validation failed: %w", err)
	}
	return nil
}
