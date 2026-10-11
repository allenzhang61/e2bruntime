package main

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"sort"
	"strings"
)

func expired(keys []string, keep int) []string {
	sort.Strings(keys)
	if len(keys) <= keep {
		return nil
	}
	return keys[:len(keys)-keep]
}
func (w *worker) retain(ctx context.Context, prefix string) (deleted, count int, err error) {
	// Only this sandbox's immutable archive objects can be removed; no shared layers.
	var keys []string
	metadataObjects := map[string]bool{}
	archives := map[string]bool{}
	p := s3.NewListObjectsV2Paginator(w.s3, &s3.ListObjectsV2Input{Bucket: aws.String(w.cfg.Bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, e := p.NextPage(ctx)
		if e != nil {
			return 0, 0, fmt.Errorf("S3 LIST failed (%T)", e)
		}
		for _, o := range page.Contents {
			k := aws.ToString(o.Key)
			if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, ".tar.gz") {
				keys = append(keys, k)
				archives[k] = true
			}
			if strings.HasPrefix(k, prefix) && strings.HasSuffix(k, ".metadata.json") {
				metadataObjects[k] = true
			}
		}
	}
	for _, k := range expired(keys, w.cfg.Keep) {
		_, e := w.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(k)})
		if e != nil {
			return deleted, len(keys) - deleted, fmt.Errorf("S3 DELETE failed (%T)", e)
		}
		_, e = w.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(metadataKey(k))})
		if e != nil {
			return deleted, len(keys) - deleted, fmt.Errorf("metadata S3 DELETE failed (%T)", e)
		}
		delete(metadataObjects, metadataKey(k))
		deleted++
	}
	// Retry sidecar cleanup after an earlier partial deletion/upload failure.
	for k := range metadataObjects {
		archive := strings.TrimSuffix(k, ".metadata.json") + ".tar.gz"
		if archives[archive] {
			continue
		}
		if _, e := w.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(k)}); e != nil {
			return deleted, len(keys) - deleted, fmt.Errorf("orphan metadata S3 DELETE failed (%T)", e)
		}
	}
	return deleted, len(keys) - deleted, nil
}
