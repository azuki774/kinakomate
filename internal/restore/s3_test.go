package restore

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/azuki774/kinakomate/internal/config"
)

// fakeS3 simulates the S3 API. The body returned by GetObject is the exact
// bytes supplied by the test.
type fakeS3 struct {
	body    []byte
	etag    string
	size    int64
	getErr  error
	headErr error
	gotGet  bool
	gotHead bool
}

func (f *fakeS3) HeadObject(_ context.Context, _ *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.gotHead = true
	if f.headErr != nil {
		return nil, f.headErr
	}
	return &s3.HeadObjectOutput{
		ETag:          aws.String(f.etag),
		ContentLength: aws.Int64(f.size),
	}, nil
}

func (f *fakeS3) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.gotGet = true
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &s3.GetObjectOutput{
		Body:          io.NopCloser(bytes.NewReader(f.body)),
		ETag:          aws.String(f.etag),
		ContentLength: aws.Int64(f.size),
	}, nil
}

func testCfg() *config.Config {
	return &config.Config{
		S3Endpoint: "https://s3.example.com",
		S3Region:   "us-east-1",
		S3Bucket:   "backups",
		S3Key:      "misskey/daily/dump.sql.gz",
	}
}

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestCheckConnection_HeadOk(t *testing.T) {
	fake := &fakeS3{etag: "\"abc\"", size: 42}
	o := newObjectStorageWithClient(fake)

	if err := o.CheckConnection(context.Background(), testCfg()); err != nil {
		t.Fatalf("CheckConnection returned error: %v", err)
	}
	if !fake.gotHead {
		t.Fatal("expected HeadObject to be called")
	}
}

func TestCheckConnection_HeadError(t *testing.T) {
	fake := &fakeS3{headErr: errors.New("head boom")}
	o := newObjectStorageWithClient(fake)

	if err := o.CheckConnection(context.Background(), testCfg()); err == nil {
		t.Fatal("expected error on HeadObject failure")
	}
}

func TestDownloadAndExtract_StagesGzip(t *testing.T) {
	sql := []byte("SELECT 1;\nCREATE TABLE t(id int);\n")
	fake := &fakeS3{body: gzipBytes(t, sql), etag: "\"abc\"", size: int64(len(sql))}
	o := newObjectStorageWithClient(fake)

	dump, err := o.DownloadAndExtract(context.Background(), testCfg())
	if err != nil {
		t.Fatalf("DownloadAndExtract returned error: %v", err)
	}
	t.Cleanup(dump.Cleanup)

	if !fake.gotGet {
		t.Fatal("expected GetObject to be called")
	}
	if dump.Bucket != "backups" || dump.Key != "misskey/daily/dump.sql.gz" {
		t.Errorf("dump bucket/key = %q/%q, want backups/misskey/daily/dump.sql.gz", dump.Bucket, dump.Key)
	}
	if dump.ETag != "\"abc\"" {
		t.Errorf("dump etag = %q, want \"abc\"", dump.ETag)
	}
	if dump.Size != int64(len(sql)) {
		t.Errorf("dump size = %d, want %d", dump.Size, len(sql))
	}

	// The staged file must be a valid gzip that decompresses back to SQL, and it
	// must never have been expanded to a plaintext file.
	data, err := os.ReadFile(dump.Path)
	if err != nil {
		t.Fatalf("read staged dump: %v", err)
	}
	if !bytes.Equal(data, fake.body) {
		t.Errorf("staged file does not match gzip body")
	}
}

func TestDownloadAndExtract_InvalidGzip(t *testing.T) {
	fake := &fakeS3{body: []byte("not a gzip stream"), etag: "\"abc\"", size: 15}
	o := newObjectStorageWithClient(fake)

	if _, err := o.DownloadAndExtract(context.Background(), testCfg()); err == nil {
		t.Fatal("expected error on invalid gzip")
	}
}

func TestDownloadAndExtract_TruncatedGzip(t *testing.T) {
	full := gzipBytes(t, []byte("SELECT 1;\n"))
	fake := &fakeS3{body: full[:len(full)-6], etag: "\"abc\"", size: int64(len(full))}
	o := newObjectStorageWithClient(fake)

	if _, err := o.DownloadAndExtract(context.Background(), testCfg()); err == nil {
		t.Fatal("expected error on truncated gzip")
	}
}

func TestDownloadAndExtract_GetError(t *testing.T) {
	fake := &fakeS3{getErr: errors.New("get boom")}
	o := newObjectStorageWithClient(fake)

	if _, err := o.DownloadAndExtract(context.Background(), testCfg()); err == nil {
		t.Fatal("expected error on GetObject failure")
	}
}
