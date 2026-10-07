package backup

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type cancelingTransferClient struct {
	transfermanager.S3APIClient
	cancel           context.CancelFunc
	abortContextErr  error
	abortDeadline    time.Time
	hasAbortDeadline bool
}

func (c *cancelingTransferClient) CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String("upload-id")}, nil
}

func (c *cancelingTransferClient) UploadPart(ctx context.Context, _ *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	c.cancel()
	return nil, ctx.Err()
}

func (c *cancelingTransferClient) AbortMultipartUpload(ctx context.Context, _ *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	c.abortContextErr = ctx.Err()
	c.abortDeadline, c.hasAbortDeadline = ctx.Deadline()
	return &s3.AbortMultipartUploadOutput{}, nil
}

type repeatingReader struct {
	remaining int64
}

func (r *repeatingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:int(r.remaining)]
	}
	for i := range p {
		p[i] = 'x'
	}
	r.remaining -= int64(len(p))
	return len(p), nil
}

func TestTransferUploaderAbortsMultipartUploadAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &cancelingTransferClient{cancel: cancel}
	uploader := newTransferUploader(client)
	_, err := uploader.UploadObject(ctx, &transfermanager.UploadObjectInput{
		Bucket: aws.String("bucket"),
		Key:    aws.String("backup.gz"),
		Body:   &repeatingReader{remaining: uploadPartSize + 1},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("UploadObject() error = %v, want context canceled", err)
	}
	if client.abortContextErr != nil {
		t.Fatal("multipart abort used a canceled context")
	}
	if !client.hasAbortDeadline {
		t.Fatal("multipart abort context has no timeout")
	}
	remaining := time.Until(client.abortDeadline)
	if remaining <= 0 || remaining > uploadFailureTimeout {
		t.Fatal("multipart abort context has an invalid timeout")
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("test did not cancel the transfer context")
	}
}
