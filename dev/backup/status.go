package main

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func (w *worker) status(ctx context.Context) error {
	running, e := w.api.running(ctx)
	if e != nil {
		return e
	}
	for _, sb := range running {
		fmt.Printf("node=%s running sandbox=%s purpose=%s\n", w.cfg.Node, sb.ID, sb.Metadata["purpose"])
	}
	var bytes int64
	count := 0
	p := s3.NewListObjectsV2Paginator(w.s3, &s3.ListObjectsV2Input{Bucket: aws.String(w.cfg.Bucket), Prefix: aws.String("backups/")})
	for p.HasMorePages() {
		page, e := p.NextPage(ctx)
		if e != nil {
			return fmt.Errorf("LIST failed (%T)", e)
		}
		for _, o := range page.Contents {
			parts := backupParts(aws.ToString(o.Key))
			if len(parts) < 2 || parts[1] != w.cfg.Node {
				continue
			}
			fmt.Printf("object=%s bytes=%d\n", aws.ToString(o.Key), aws.ToInt64(o.Size))
			bytes += aws.ToInt64(o.Size)
			count++
		}
	}
	fmt.Printf("node=%s objects=%d total_bytes=%d\n", w.cfg.Node, count, bytes)
	return nil
}
