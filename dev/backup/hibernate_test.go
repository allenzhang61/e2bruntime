package main

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBackupBeforeHibernate(t *testing.T) {
	for _, fail := range []bool{false, true} {
		var events []string
		err := backupBeforeHibernate(context.Background(), func(context.Context) error {
			events = append(events, "backup")
			if fail {
				return errors.New("S3 unavailable")
			}
			return nil
		}, func(context.Context) error { events = append(events, "pause"); return nil })
		if fail && (err == nil || len(events) != 1) {
			t.Fatalf("pause after failed backup: %v %v", events, err)
		}
		if !fail && (err != nil || len(events) != 2 || events[1] != "pause") {
			t.Fatalf("wrong ordering: %v %v", events, err)
		}
	}
}
func TestBackupCancellationPreventsHibernate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := backupBeforeHibernate(ctx, func(context.Context) error { cancel(); return nil }, func(context.Context) error { t.Fatal("paused after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestNodeLockWaitHonorsCancellation(t *testing.T) {
	w := worker{cfg: config{Node: "test-" + t.Name()}}
	err := w.withNodeLock(context.Background(), func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		return w.withNodeLock(ctx, func() error { t.Fatal("overlapping node operations"); return nil })
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestHibernateLive(t *testing.T) {
	if os.Getenv("HIBERNATE_LIVE") != "1" {
		t.Skip("set HIBERNATE_LIVE=1 on Runtime node")
	}
	c, err := configInit()
	if err != nil {
		t.Fatal(err)
	}
	w := worker{cfg: c, api: newAPI(c), s3: s3.NewFromConfig(aws.Config{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AK, c.SK, "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.Endpoint); o.UsePathStyle = true })}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var sb sandbox
	_, err = w.api.call(ctx, "POST", "/v2/sandboxes", map[string]any{"templateID": "base", "timeout": 600}, &sb)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		w.api.call(clean, "DELETE", "/sandboxes/"+sb.ID, nil, nil)
		prefix := "backups/" + c.Node + "/" + sb.ID + "/"
		pg := s3.NewListObjectsV2Paginator(w.s3, &s3.ListObjectsV2Input{Bucket: aws.String(c.Bucket), Prefix: aws.String(prefix)})
		for pg.HasMorePages() {
			p, e := pg.NextPage(clean)
			if e != nil {
				t.Error(e)
				break
			}
			for _, obj := range p.Contents {
				if _, e := w.s3.DeleteObject(clean, &s3.DeleteObjectInput{Bucket: aws.String(c.Bucket), Key: obj.Key}); e != nil {
					t.Error(e)
				}
			}
		}
	}()
	if _, err = w.api.RunCommand(ctx, sb, "printf 'final-backup-check' > /home/user/hibernate-check.txt; sync"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err = w.hibernate(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}
	t.Logf("backup+hibernate_ms=%.2f", float64(time.Since(start).Microseconds())/1000)
	var paused []sandbox
	if _, err = w.api.call(ctx, "GET", "/v2/sandboxes?state=paused&limit=100", nil, &paused); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range paused {
		if p.ID == sb.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("sandbox not registered paused")
	}
	if _, err = w.api.call(ctx, "POST", "/v2/sandboxes/"+sb.ID+"/connect", map[string]int{"timeout": 300}, &sb); err != nil {
		t.Fatal(err)
	}
	result, err := w.api.RunCommand(ctx, sb, "cat /home/user/hibernate-check.txt")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Stdout) != "final-backup-check" {
		t.Fatal("file mismatch after wake")
	}
	t.Log("S3 final backup verified; paused state confirmed; wake and file read passed")
}
