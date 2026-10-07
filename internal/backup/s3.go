package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/azuki774/kinakomate/internal/config"
	"github.com/azuki774/kinakomate/internal/storage"
)

const (
	uploadPartSize       = 5 * 1024 * 1024
	uploadConcurrency    = 2
	uploadFailureTimeout = 15 * time.Second
)

type objectStore interface {
	Upload(context.Context, string, string, io.ReadSeeker) error
	HeadObjectSize(context.Context, string, string) (int64, error)
}

type dumpFile interface {
	io.Writer
	io.ReadSeeker
	Chmod(os.FileMode) error
	Close() error
	Name() string
}

type openedDump interface {
	io.ReadSeeker
	Close() error
}

type s3ObjectStore struct {
	client   *s3.Client
	uploader *transfermanager.Client
}

func newS3ObjectStore(ctx context.Context, cfg *config.BackupConfig) (objectStore, error) {
	client, err := storage.NewS3Client(ctx, cfg.S3Region, cfg.S3Endpoint)
	if err != nil {
		return nil, err
	}

	return &s3ObjectStore{
		client:   client,
		uploader: newTransferUploader(client),
	}, nil
}

func newTransferUploader(client transfermanager.S3APIClient) *transfermanager.Client {
	return transfermanager.New(client, func(options *transfermanager.Options) {
		options.PartSizeBytes = uploadPartSize
		options.MultipartUploadThreshold = uploadPartSize
		options.Concurrency = uploadConcurrency
		options.FailTimeout = uploadFailureTimeout
	})
}

func (s *s3ObjectStore) Upload(ctx context.Context, bucket, key string, body io.ReadSeeker) error {
	_, err := s.uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   body,
	})
	return err
}

func (s *s3ObjectStore) HeadObjectSize(ctx context.Context, bucket, key string) (int64, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil || out == nil || out.ContentLength == nil {
		return 0, errors.New("S3 object size unavailable")
	}
	return *out.ContentLength, nil
}
