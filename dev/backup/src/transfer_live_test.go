package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func transferWorker(t *testing.T) *worker {
	t.Helper()
	c, err := configInit()
	if err != nil {
		t.Fatal(err)
	}
	return &worker{cfg: c, api: newAPI(c), s3: s3.NewFromConfig(aws.Config{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AK, c.SK, "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.Endpoint); o.UsePathStyle = true })}
}
func TestOwnershipCASLive(t *testing.T) {
	if os.Getenv("OWNERSHIP_LIVE") != "1" {
		t.Skip("set OWNERSHIP_LIVE=1")
	}
	w := transferWorker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := fmt.Sprintf("ownershipcheck%d", time.Now().UnixNano())
	key := ownershipKey(w.cfg.Node, id)
	defer w.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(key)})
	owner := backupOwnership{SchemaVersion: 1, LogicalNode: w.cfg.Node, LogicalSandboxID: id, OwnerNode: w.cfg.Node, OwnerSandboxID: id, State: "active", Generation: 1}
	etag, err := w.writeOwner(ctx, owner, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.writeOwner(ctx, owner, ""); err == nil {
		t.Fatal("RustFS ignored If-None-Match")
	}
	owner.Generation++
	if _, err = w.writeOwner(ctx, owner, `"wrong"`); err == nil {
		t.Fatal("RustFS ignored If-Match")
	}
	if _, err = w.writeOwner(ctx, owner, etag); err != nil {
		t.Fatal(err)
	}
	t.Log("RustFS conditional ownership writes verified")
}
func TestTransferSeedLive(t *testing.T) {
	if os.Getenv("TRANSFER_SEED_LIVE") != "1" {
		t.Skip("set TRANSFER_SEED_LIVE=1 on runtime1")
	}
	w := transferWorker(t)
	if w.cfg.Node != "runtime1" {
		t.Fatal("source must be runtime1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	marker := fmt.Sprintf("transfer-%d", time.Now().UnixNano())
	var sb sandbox
	_, err := w.api.call(ctx, "POST", "/v2/sandboxes", map[string]any{"templateID": "base", "timeout": 600, "metadata": map[string]string{"purpose": "transfer-validation", "transfer_marker": marker}}, &sb)
	if err != nil {
		t.Fatal(err)
	}
	defer w.api.call(ctx, "DELETE", "/sandboxes/"+sb.ID, nil, nil)
	_, err = w.api.RunCommand(ctx, sb, "printf '%s' '"+marker+"' > /home/user/transfer-marker.txt; sync")
	if err != nil {
		t.Fatal(err)
	}
	if err = w.backup(ctx, sb.ID); err != nil {
		t.Fatal(err)
	}
	all, err := w.exportLatest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	entries := []transferEntry{}
	for _, e := range all {
		if e.SandboxID == sb.ID {
			entries = append(entries, e)
		}
	}
	if len(entries) != 1 || !entries[0].Importable {
		t.Fatal("seed not exported")
	}
	if err = saveJSON("/opt/e2b-runtime/transfer-fixture.json", entries); err != nil {
		t.Fatal(err)
	}
	t.Logf("fixture sandbox=%s archive=%s", sb.ID, entries[0].ArchiveKey)
}
func TestTransferVerifyLive(t *testing.T) {
	if os.Getenv("TRANSFER_VERIFY_LIVE") != "1" {
		t.Skip("set TRANSFER_VERIFY_LIVE=1 on runtime2")
	}
	w := transferWorker(t)
	if w.cfg.Node != "runtime2" {
		t.Fatal("target must be runtime2")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	data, err := os.ReadFile(filepath.Join(w.cfg.ImportJobs, os.Getenv("TRANSFER_JOB")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var job importJob
	if json.Unmarshal(data, &job) != nil || job.Status != "completed" || len(job.Results) != 1 || job.Results[0].Status != "succeeded" {
		t.Fatal("import not completed")
	}
	result := job.Results[0]
	var entries []transferEntry
	raw, err := os.ReadFile("/opt/e2b-runtime/transfer-fixture.json")
	if err != nil || json.Unmarshal(raw, &entries) != nil || len(entries) != 1 {
		t.Fatal("fixture missing")
	}
	marker := entries[0].Metadata.Sandbox.Metadata["transfer_marker"]
	var sb sandbox
	_, err = w.api.call(ctx, "POST", "/sandboxes/"+result.SandboxID+"/connect", map[string]int{"timeout": 300}, &sb)
	if err != nil {
		t.Fatal(err)
	}
	defer w.api.call(ctx, "DELETE", "/sandboxes/"+sb.ID, nil, nil)
	command, err := w.api.RunCommand(ctx, sb, "cat /home/user/transfer-marker.txt")
	if err != nil || strings.TrimSpace(command.Stdout) != marker {
		t.Fatal("restored marker mismatch", err)
	}
	owner, etag, err := w.readOwner(ctx, entries[0].SourceNode, entries[0].SandboxID)
	if err != nil || owner.OwnerNode != "runtime2" || owner.OwnerSandboxID != sb.ID || owner.State != "active" {
		t.Fatal("target backup ownership mismatch")
	}
	source := *w
	source.cfg.Node = "runtime1"
	if _, _, err = source.backupPermission(ctx, entries[0].Metadata.Sandbox); err == nil {
		t.Fatal("source retained backup permission")
	}
	owner.State = "retired"
	owner.Generation++
	if _, err = w.writeOwner(ctx, owner, etag); err != nil {
		t.Fatal(err)
	}
	t.Logf("cross Runtime marker verified sandbox=%s duration_ms=%.2f; disposable target deleted", sb.ID, result.DurationMS)
}

func TestTransferRejectForeignLive(t *testing.T) {
	if os.Getenv("TRANSFER_REJECT_LIVE") != "1" {
		t.Skip("set TRANSFER_REJECT_LIVE=1 on runtime2")
	}
	w := transferWorker(t)
	if w.cfg.Node != "runtime2" {
		t.Fatal("target must be runtime2")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := os.ReadFile("/opt/e2b-runtime/transfer-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:3012/imports", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-API-Key", w.cfg.Key)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var job importJob
	if response.StatusCode != 202 || json.NewDecoder(response.Body).Decode(&job) != nil {
		t.Fatal("job submission failed", response.StatusCode)
	}
	for {
		data, err := os.ReadFile(filepath.Join(w.cfg.ImportJobs, job.ID+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(data, &job) != nil {
			t.Fatal("invalid job")
		}
		if job.Status == "completed_with_errors" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("job did not finish")
		case <-time.After(100 * time.Millisecond):
		}
	}
	if len(job.Results) != 1 || job.Results[0].Status != "failed" || !strings.Contains(job.Results[0].Error, "not assigned to this Runtime") || job.Results[0].SandboxID != "" {
		t.Fatalf("wrong ownership was not rejected: %+v", job)
	}
	t.Logf("non-owned snapshot rejected before sandbox creation; job=%s", job.ID)
}

func TestTransferImportLive(t *testing.T) {
	if os.Getenv("TRANSFER_IMPORT_LIVE") != "1" {
		t.Skip("set TRANSFER_IMPORT_LIVE=1 on runtime2")
	}
	w := transferWorker(t)
	if w.cfg.Node != "runtime2" {
		t.Fatal("target must be runtime2")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	raw, err := os.ReadFile("/opt/e2b-runtime/transfer-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []transferEntry
	if json.Unmarshal(raw, &entries) != nil || len(entries) != 1 {
		t.Fatal("invalid fixture")
	}
	if err = w.takeover(ctx, entries[0]); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:3012/imports", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-API-Key", w.cfg.Key)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var job importJob
	if (response.StatusCode != 202 && response.StatusCode != 200) || json.NewDecoder(response.Body).Decode(&job) != nil {
		t.Fatal("job submission failed", response.StatusCode)
	}
	for {
		data, err := os.ReadFile(filepath.Join(w.cfg.ImportJobs, job.ID+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(data, &job) != nil {
			t.Fatal("invalid job")
		}
		if strings.HasPrefix(job.Status, "completed") {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("job did not finish")
		case <-time.After(250 * time.Millisecond):
		}
	}
	if len(job.Results) != 1 || job.Status != "completed" || job.Results[0].Status != "succeeded" {
		t.Fatalf("import failed: %+v", job)
	}
	t.Logf("takeover and HTTP import succeeded sandbox=%s duration_ms=%.2f job=%s", job.Results[0].SandboxID, job.Results[0].DurationMS, job.ID)
	if err = os.WriteFile("/opt/e2b-runtime/transfer-job-id", []byte(job.ID), 0600); err != nil {
		t.Fatal(err)
	}
}
