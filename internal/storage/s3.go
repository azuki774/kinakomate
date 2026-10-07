package storage

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// NewS3Client builds an S3 client using the AWS SDK credential chain. A custom
// endpoint is accessed in path-style; an empty endpoint selects AWS defaults.
func NewS3Client(ctx context.Context, region, endpoint string) (*s3.Client, error) {
	var awsCfg aws.Config
	var err error
	if region == "" {
		awsCfg, err = awsconfig.LoadDefaultConfig(ctx)
	} else {
		awsCfg, err = awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	}
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = endpoint != ""
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	}), nil
}
