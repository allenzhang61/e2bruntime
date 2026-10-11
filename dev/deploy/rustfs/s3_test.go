package rustfs_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type config struct{ Endpoint, AK, SK, Bucket, Region string }

// All required environment variables and their examples are declared here.
func configInit(t *testing.T) config {
	t.Helper()
	cfg := config{
		Endpoint: os.Getenv("RUSTFS_ENDPOINT"),    // Example: http://192.168.1.211:19000
		AK:       os.Getenv("RUSTFS_ACCESS_KEY"),  // Example: E2B<generated-uppercase-hex>
		SK:       os.Getenv("RUSTFS_SECRET_KEY"),  // Example: <generated-secret>; never print
		Bucket:   os.Getenv("RUSTFS_BUCKET_NAME"), // Example: runtime-snapshots
		Region:   os.Getenv("RUSTFS_REGION"),      // Default: us-east-1
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.Endpoint == "" || cfg.AK == "" || cfg.SK == "" || cfg.Bucket == "" {
		t.Fatal("set RUSTFS_ENDPOINT, RUSTFS_ACCESS_KEY, RUSTFS_SECRET_KEY and RUSTFS_BUCKET_NAME (deploy.sh reads .env)")
	}
	return cfg
}

// Provision the bucket, then verify S3 PUT/GET/HEAD/DELETE using the official Go AWS SDK.
func TestS3(t *testing.T) {
	if testing.Short() {
		t.Skip("live S3 integration test")
	}
	cfg := configInit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client := s3.NewFromConfig(aws.Config{Region: cfg.Region, Credentials: credentials.NewStaticCredentialsProvider(cfg.AK, cfg.SK, "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(cfg.Endpoint); o.UsePathStyle = true })
	// Error details can include request metadata. Report operation and error code only.
	check := func(operation string, err error) {
		t.Helper()
		if err != nil {
			var api smithy.APIError
			if errors.As(err, &api) {
				t.Fatalf("%s failed: %s", operation, api.ErrorCode())
			}
			t.Fatalf("%s failed (%T)", operation, err)
		}
	}
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(cfg.Bucket)})
	if err != nil {
		var api smithy.APIError
		if !errors.As(err, &api) || (api.ErrorCode() != "NotFound" && api.ErrorCode() != "NoSuchBucket" && api.ErrorCode() != "404") {
			check("HEAD bucket", err)
		}
		_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(cfg.Bucket)})
		check("create bucket", err)
	}
	t.Logf("bucket ready: %s, endpoint: %s", cfg.Bucket, cfg.Endpoint)
	nonce := make([]byte, 16)
	_, err = rand.Read(nonce)
	check("random key", err)
	key := "checks/" + hex.EncodeToString(nonce) + ".bin"
	payload := make([]byte, 1<<20)
	_, err = rand.Read(payload)
	check("random payload", err)
	// Independent cleanup even on validation failure or main context expiry.
	deleted := false
	t.Cleanup(func() {
		if !deleted {
			clean, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err := client.DeleteObject(clean, &s3.DeleteObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key)})
			if err != nil {
				t.Errorf("cleanup object failed (%T)", err)
			}
		}
	})
	start := time.Now()
	_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key), Body: bytes.NewReader(payload)})
	check("upload", err)
	put := time.Since(start)
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key)})
	check("HEAD object", err)
	if aws.ToInt64(head.ContentLength) != int64(len(payload)) {
		t.Fatal("object size mismatch")
	}
	start = time.Now()
	got, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key)})
	check("download", err)
	actual, err := io.ReadAll(io.LimitReader(got.Body, int64(len(payload))+1))
	got.Body.Close()
	check("read downloaded object", err)
	if !bytes.Equal(actual, payload) {
		t.Fatal("downloaded bytes differ")
	}
	download := time.Since(start)
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key)})
	check("delete", err)
	deleted = true
	_, err = client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(key)})
	var api smithy.APIError
	if !errors.As(err, &api) || (api.ErrorCode() != "NotFound" && api.ErrorCode() != "NoSuchKey" && api.ErrorCode() != "404") {
		t.Fatal("deleted object must return not found")
	}
	t.Logf("S3 verified: %d bytes, SHA256=%x, PUT=%s, GET+verify=%s; temporary object removed", len(payload), sha256.Sum256(payload), put, download)
}

// Explicitly verify that a reset environment contains no previous test objects.
func TestEmptyBucket(t *testing.T) {
	if os.Getenv("RUSTFS_VERIFY_EMPTY") != "1" {
		t.Skip("set RUSTFS_VERIFY_EMPTY=1 after a clean rebuild")
	}
	cfg := configInit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := s3.NewFromConfig(aws.Config{Region: cfg.Region, Credentials: credentials.NewStaticCredentialsProvider(cfg.AK, cfg.SK, "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(cfg.Endpoint); o.UsePathStyle = true })
	result, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(cfg.Bucket), MaxKeys: aws.Int32(1)})
	if err != nil {
		t.Fatalf("list reset bucket failed (%T)", err)
	}
	if len(result.Contents) != 0 || aws.ToBool(result.IsTruncated) {
		t.Fatal("reset bucket still contains objects")
	}
	t.Log("reset bucket verified: 0 objects")
}
