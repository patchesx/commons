package s3storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

var (
	mu        sync.Mutex
	pkgPool   *pgxpool.Pool
	pkgEncKey []byte
)

// Init stores the pool and encryption key for use by package-level functions.
func Init(pool *pgxpool.Pool, encKey []byte) {
	mu.Lock()
	pkgPool = pool
	pkgEncKey = encKey
	mu.Unlock()
}

type s3Credentials struct {
	endpointURL     string
	bucket          string
	region          string
	accessKeyID     string
	secretAccessKey string
}

func getCredentials(ctx context.Context, pool *pgxpool.Pool, encKey []byte) (*s3Credentials, error) {
	get := func(key string) (string, error) {
		return store.GetServiceConfig(ctx, pool, "s3", key, encKey)
	}

	endpointURL, _ := get("endpoint_url") // optional
	bucket, err := get("bucket")
	if err != nil {
		return nil, fmt.Errorf("s3/bucket: %w", err)
	}
	region, _ := get("region") // optional; default applied below
	if region == "" {
		region = "us-east-1"
	}
	accessKeyID, err := get("access_key_id")
	if err != nil {
		return nil, fmt.Errorf("s3/access_key_id: %w", err)
	}
	secretAccessKey, err := get("secret_access_key")
	if err != nil {
		return nil, fmt.Errorf("s3/secret_access_key: %w", err)
	}

	return &s3Credentials{
		endpointURL:     endpointURL,
		bucket:          bucket,
		region:          region,
		accessKeyID:     accessKeyID,
		secretAccessKey: secretAccessKey,
	}, nil
}

// testHTTPClient, when non-nil, overrides the HTTP client used by the S3 SDK.
// Set only by tests, to trust an httptest TLS server's certificate.
var testHTTPClient *http.Client

func newS3Client(ctx context.Context, creds *s3Credentials) (*s3.Client, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(creds.region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(creds.accessKeyID, creds.secretAccessKey, ""),
		),
	}
	if testHTTPClient != nil {
		loadOpts = append(loadOpts, awsconfig.WithHTTPClient(testHTTPClient))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("build S3 config: %w", err)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		// Only compute/validate checksums when an operation requires them. The
		// SDK's "WhenSupported" default (since Jan 2025) breaks streaming
		// (unseekable) uploads without TLS and is rejected by many
		// S3-compatible backends (MinIO, B2, R2).
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
		o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		if creds.endpointURL != "" {
			o.BaseEndpoint = aws.String(creds.endpointURL)
			// Path-style addressing is required for MinIO and most S3-compatible services.
			o.UsePathStyle = true
		}
	})
	return client, nil
}

// UploadObject streams r to the given S3 key in the configured bucket.
func UploadObject(ctx context.Context, pool *pgxpool.Pool, encKey []byte, key string, r io.Reader, contentLength int64) error {
	creds, err := getCredentials(ctx, pool, encKey)
	if err != nil {
		return err
	}
	client, err := newS3Client(ctx, creds)
	if err != nil {
		return err
	}

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(creds.bucket),
		Key:           aws.String(key),
		Body:          r,
		ContentLength: aws.Int64(contentLength),
	})
	if err != nil {
		return fmt.Errorf("S3 PutObject %s: %w", key, err)
	}
	return nil
}
