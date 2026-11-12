package events

import (
	"bytes"
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Object is one file the Writer uploads.
type Object struct {
	Key             string
	Body            []byte
	ContentType     string
	ContentEncoding string
}

// Target is where the Writer uploads: an S3 bucket or a local directory. Its
// name is how spooled files find it again after a restart.
type Target interface {
	Name() string
	Put(ctx context.Context, o Object) error
}

// s3PutObjectAPI is the one S3 call the writer makes, so tests can inject a
// fake without a live endpoint.
type s3PutObjectAPI interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// S3Target uploads to an S3-compatible bucket: AWS S3 (credentials from the
// default chain: IRSA or Pod Identity, so no keys in the cluster) or
// DigitalOcean Spaces and MinIO (an Endpoint and static keys).
type S3Target struct {
	name   string
	client s3PutObjectAPI
	bucket string
}

// S3TargetConfig configures an S3Target.
type S3TargetConfig struct {
	// Bucket is the S3 bucket / DO Space name. Required.
	Bucket string
	// Region is the store region. Required.
	Region string
	// Endpoint overrides the S3 endpoint for S3-compatible stores (DO Spaces,
	// MinIO). Empty => native AWS S3.
	Endpoint string
	// AccessKeyID / SecretAccessKey are static credentials for stores without
	// IRSA. Leave both empty on AWS to use the default credential chain.
	AccessKeyID     string
	SecretAccessKey string
	// ForcePathStyle addresses buckets as <endpoint>/<bucket>, as MinIO and
	// some S3-compatible stores require.
	ForcePathStyle bool
}

// NewS3Target builds the S3 client for cfg.
func NewS3Target(ctx context.Context, name string, cfg S3TargetConfig) (*S3Target, error) {
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3 target: bucket is required")
	}
	if cfg.Region == "" {
		return nil, fmt.Errorf("s3 target: region is required")
	}
	loadOpts := []func(*config.LoadOptions) error{config.WithRegion(cfg.Region)}
	// Static creds only when provided (DO Spaces / MinIO). On AWS this stays
	// unset so the default chain resolves IRSA or Pod Identity.
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		loadOpts = append(loadOpts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("s3 target: load aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		if cfg.ForcePathStyle {
			o.UsePathStyle = true
		}
	})
	return newS3TargetWithClient(name, client, cfg.Bucket), nil
}

// newS3TargetWithClient is the test seam.
func newS3TargetWithClient(name string, client s3PutObjectAPI, bucket string) *S3Target {
	return &S3Target{name: name, client: client, bucket: bucket}
}

func (t *S3Target) Name() string { return t.name }

func (t *S3Target) Put(ctx context.Context, o Object) error {
	in := &s3.PutObjectInput{
		Bucket: aws.String(t.bucket),
		Key:    aws.String(o.Key),
		Body:   bytes.NewReader(o.Body),
	}
	if o.ContentType != "" {
		in.ContentType = aws.String(o.ContentType)
	}
	if o.ContentEncoding != "" {
		in.ContentEncoding = aws.String(o.ContentEncoding)
	}
	_, err := t.client.PutObject(ctx, in)
	return err
}
