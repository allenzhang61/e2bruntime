package main

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Clears only the explicitly named, configured project bucket; never other buckets.
func (w *worker) clearProjectBucket(ctx context.Context, name string) error {
	if name == "" || name != w.cfg.Bucket {
		return fmt.Errorf("only configured project bucket can be cleared")
	}
	objects := []types.ObjectIdentifier{}
	pages := s3.NewListObjectsV2Paginator(w.s3, &s3.ListObjectsV2Input{Bucket: aws.String(name)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("list bucket %s failed", name)
		}
		for _, obj := range page.Contents {
			objects = append(objects, types.ObjectIdentifier{Key: obj.Key})
		}
	}
	for start := 0; start < len(objects); start += 1000 {
		end := start + 1000
		if end > len(objects) {
			end = len(objects)
		}
		out, err := w.s3.DeleteObjects(ctx, &s3.DeleteObjectsInput{Bucket: aws.String(name), Delete: &types.Delete{Objects: objects[start:end], Quiet: aws.Bool(true)}})
		if err != nil || len(out.Errors) > 0 {
			return fmt.Errorf("delete bucket %s failed", name)
		}
	}
	remaining, err := w.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(name), MaxKeys: aws.Int32(1)})
	if err != nil || len(remaining.Contents) > 0 {
		return fmt.Errorf("bucket %s is not empty", name)
	}
	fmt.Printf("bucket=%s deleted_objects=%d remaining=0\n", name, len(objects))
	return nil
}
