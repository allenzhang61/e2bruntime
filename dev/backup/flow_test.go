package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type flowConfig struct {
	config
	Wait   time.Duration
	Report string
}

// All dependencies: base configInit plus FLOW_LIVE=1, FLOW_WAIT=3m,
// FLOW_REPORT=/opt/e2b-runtime/flow-result.json. Run on runtime1 with its worker active.
func flowConfigInit() (flowConfig, error) {
	c, e := configInit()
	if e != nil {
		return flowConfig{}, e
	}
	wait, e := time.ParseDuration(env("FLOW_WAIT", "3m"))
	if e != nil || wait < 3*time.Minute || wait > 10*time.Minute {
		return flowConfig{}, fmt.Errorf("FLOW_WAIT must be between 3m and 10m")
	}
	return flowConfig{c, wait, env("FLOW_REPORT", "/opt/e2b-runtime/flow-result.json")}, nil
}
func flowObjects(ctx context.Context, w *worker, id string) ([]restoreFile, error) {
	prefix := "backups/" + w.cfg.Node + "/" + id + "/filesystem/"
	pg := s3.NewListObjectsV2Paginator(w.s3, &s3.ListObjectsV2Input{Bucket: aws.String(w.cfg.Bucket), Prefix: aws.String(prefix)})
	var files []restoreFile
	for pg.HasMorePages() {
		p, e := pg.NextPage(ctx)
		if e != nil {
			return nil, e
		}
		for _, o := range p.Contents {
			key := aws.ToString(o.Key)
			if !strings.HasSuffix(key, ".tar.gz") {
				continue
			}
			files = append(files, restoreFile{Key: key, Bytes: aws.ToInt64(o.Size), StoredAt: aws.ToTime(o.LastModified).UTC().Format(time.RFC3339Nano)})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Key < files[j].Key })
	return files, nil
}
func TestSandboxFlowLive(t *testing.T) {
	if os.Getenv("FLOW_LIVE") != "1" {
		t.Skip("set FLOW_LIVE=1 on runtime1 with periodic backup active")
	}
	c, e := flowConfigInit()
	if e != nil {
		t.Fatal(e)
	}
	if c.Node != "runtime1" {
		t.Fatal("this flow uses runtime1's existing backup worker")
	}
	w := worker{cfg: c.config, api: newAPI(c.config), s3: s3.NewFromConfig(aws.Config{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AK, c.SK, "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.Endpoint); o.UsePathStyle = true })}
	ctx, cancel := context.WithTimeout(context.Background(), c.Wait+6*time.Minute)
	defer cancel()
	report := map[string]any{"node": c.Node, "wait_seconds": c.Wait.Seconds(), "started_at_utc": time.Now().UTC().Format(time.RFC3339Nano), "passed": false}
	defer func() {
		report["passed"] = !t.Failed()
		report["finished_at_utc"] = time.Now().UTC().Format(time.RFC3339Nano)
		data, e := json.MarshalIndent(report, "", "  ")
		if e != nil {
			t.Error(e)
			return
		}
		if e = os.WriteFile(c.Report, append(data, '\n'), 0600); e != nil {
			t.Error(e)
		}
	}()
	var sb sandbox
	if _, e = w.api.call(ctx, "POST", "/v2/sandboxes", map[string]any{"templateID": "base", "timeout": 900, "metadata": map[string]string{"purpose": "hibernate-flow-test"}}, &sb); e != nil {
		t.Fatal(e)
	}
	report["sandbox_id"] = sb.ID
	defer func() {
		clean, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		_, e := w.api.call(clean, "DELETE", "/sandboxes/"+sb.ID, nil, nil)
		if e != nil {
			t.Errorf("sandbox cleanup: %v", e)
			return
		}
		_, e = w.api.call(clean, "GET", "/sandboxes/"+sb.ID, nil, nil)
		if e == nil || !strings.HasSuffix(e.Error(), "HTTP 404") {
			t.Errorf("sandbox deletion not confirmed: %v", e)
			return
		}
		report["sandbox_deleted"] = true
		t.Log("sandbox deleted; S3 backups retained for inspection")
	}()
	expected := "flow-" + sb.ID
	if _, e = w.api.RunCommand(ctx, sb, "printf '%s' '"+expected+"' > /home/user/flow-check.txt; sync"); e != nil {
		t.Fatal(e)
	}
	t.Logf("created sandbox=%s; waiting %s before hibernate", sb.ID, c.Wait)
	deadline := time.Now().Add(c.Wait)
	for time.Now().Before(deadline) {
		d := time.Until(deadline)
		if d > 30*time.Second {
			d = 30 * time.Second
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(d):
		}
		files, e := flowObjects(ctx, &w, sb.ID)
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("elapsed waiting=%s automatic backups=%d", c.Wait-time.Until(deadline), len(files))
	}
	before, e := flowObjects(ctx, &w, sb.ID)
	if e != nil {
		t.Fatal(e)
	}
	report["automatic_backups_before_hibernate"] = before
	if len(before) < 1 {
		t.Fatal("no automatic backup during 3-minute wait; check e2b-backup.service")
	}
	start := time.Now()
	if e = w.hibernate(ctx, sb.ID); e != nil {
		t.Fatal(e)
	}
	report["backup_and_hibernate_ms"] = float64(time.Since(start).Microseconds()) / 1000
	var paused []sandbox
	if _, e = w.api.call(ctx, "GET", "/v2/sandboxes?state=paused&limit=100", nil, &paused); e != nil {
		t.Fatal(e)
	}
	found := false
	for _, p := range paused {
		if p.ID == sb.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("paused state not confirmed")
	}
	report["paused_verified"] = true
	after, e := flowObjects(ctx, &w, sb.ID)
	if e != nil {
		t.Fatal(e)
	}
	report["backups_after_hibernate"] = after
	if len(after) < len(before)+1 {
		t.Fatal("no new final backup after hibernate")
	}
	stage, e := os.MkdirTemp(filepath.Dir(c.Storage), "flow-verify-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(stage)
	// Importer checks archive bytes, manifest file hashes, and all parent-layer dependencies.
	for _, f := range after {
		archiveStage, err := os.MkdirTemp(stage, "archive-")
		if err != nil {
			t.Fatal(err)
		}
		im := importer{client: w.s3, bucket: c.Bucket, stage: archiveStage, seen: map[string]bool{}}
		if _, e = im.download(ctx, f.Key); e != nil {
			t.Fatal(e)
		}
		os.RemoveAll(archiveStage)
		t.Logf("verified backup key=%s bytes=%d saved=%s", f.Key, f.Bytes, f.StoredAt)
	}
	report["s3_archives_and_dependencies_verified"] = true
	start = time.Now()
	if _, e = w.api.call(ctx, "POST", "/v2/sandboxes/"+sb.ID+"/connect", map[string]int{"timeout": 300}, &sb); e != nil {
		t.Fatal(e)
	}
	report["wake_ms"] = float64(time.Since(start).Microseconds()) / 1000
	got, e := w.api.RunCommand(ctx, sb, "cat /home/user/flow-check.txt")
	if e != nil {
		t.Fatal(e)
	}
	if got.Stdout != expected {
		t.Fatal("restored file does not match written content")
	}
	report["restored_file_verified"] = true
	t.Logf("flow checks passed: automatic=%d final total=%d; wake file matches", len(before), len(after))
}
