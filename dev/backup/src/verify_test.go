package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
	"os"
	"testing"
	"time"
)

func TestLatestBackupDownloadLive(t *testing.T) {
	if os.Getenv("BACKUP_VERIFY_LIVE") != "1" {
		t.Skip("set BACKUP_VERIFY_LIVE=1")
	}
	c, e := configInit()
	if e != nil {
		t.Fatal(e)
	}
	client := s3.NewFromConfig(aws.Config{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AK, c.SK, "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.Endpoint); o.UsePathStyle = true })
	for _, node := range []string{"runtime1", "runtime2"} {
		t.Run(node, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			prefix := "backups/" + node + "/"
			page, e := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(c.Bucket), Prefix: aws.String(prefix)})
			if e != nil {
				t.Fatalf("LIST failed (%T)", e)
			}
			if len(page.Contents) == 0 {
				t.Fatal("no backups")
			}
			// Download every current object, including shared bases, and compare actual
			// bytes with metadata SHA-256; this is deliberately stronger than HEAD.
			for _, o := range page.Contents {
				key := aws.ToString(o.Key)
				r, e := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(key)})
				if e != nil {
					t.Fatalf("GET failed (%T)", e)
				}
				h := sha256.New()
				n, e := io.Copy(h, io.LimitReader(r.Body, 5<<30))
				r.Body.Close()
				if e != nil {
					t.Fatal(e)
				}
				if n != aws.ToInt64(o.Size) || hex.EncodeToString(h.Sum(nil)) != r.Metadata["sha256"] {
					t.Fatal("download checksum mismatch:", key)
				}
				t.Log(fmt.Sprintf("verified %s bytes=%d", key, n))
			}
		})
	}
}
