package main

import (
	"bytes"
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"os"
	"testing"
	"time"
)

func TestS3RetentionLive(t *testing.T) {
	if os.Getenv("BACKUP_LIVE") != "1" {
		t.Skip("set BACKUP_LIVE=1 for S3 integration")
	}
	c, e := configInit()
	if e != nil {
		t.Fatal(e)
	}
	c.Keep = 10
	w := worker{cfg: c, s3: s3.NewFromConfig(aws.Config{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AK, c.SK, "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.Endpoint); o.UsePathStyle = true })}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	prefix := fmt.Sprintf("checks/retention-%d/", time.Now().UnixNano())
	var keys []string
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, k := range keys {
			_, e := w.s3.DeleteObject(clean, &s3.DeleteObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(k)})
			if e != nil {
				t.Errorf("cleanup S3 failed (%T)", e)
			}
		}
	}()
	for i := 0; i < 11; i++ {
		k := fmt.Sprintf("%s%02d.tar.gz", prefix, i)
		keys = append(keys, k)
		_, e = w.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(k), Body: bytes.NewReader([]byte("retention-test"))})
		if e != nil {
			t.Fatalf("PUT failed (%T)", e)
		}
		meta := metadataKey(k)
		keys = append(keys, meta)
		if _, e = w.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(meta), Body: bytes.NewReader([]byte(`{"schema_version":1}`)), ContentType: aws.String("application/json")}); e != nil {
			t.Fatal("metadata PUT failed")
		}
	}
	deleted, kept, e := w.retain(ctx, prefix)
	if e != nil || deleted != 1 || kept != 10 {
		t.Fatalf("retention deleted=%d kept=%d error=%v", deleted, kept, e)
	}
	page, e := w.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(c.Bucket), Prefix: aws.String(prefix)})
	if e != nil {
		t.Fatalf("LIST failed (%T)", e)
	}
	if len(page.Contents) != 20 {
		t.Fatal("expected ten archive/metadata pairs")
	}
	for _, o := range page.Contents {
		if aws.ToString(o.Key) == keys[0] || aws.ToString(o.Key) == keys[1] {
			t.Fatal("oldest backup or metadata retained")
		}
	}
	t.Log("S3 retention: uploaded 11 archive/metadata pairs, retained newest 10 pairs, removed oldest pair; isolated test objects cleaned")
}
