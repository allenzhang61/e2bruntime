package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestMetadataRoundtripAndCredentialExclusion(t *testing.T) {
	raw := []byte(`{"sandboxID":"sb1","templateID":"base","cpuCount":2,"memoryMB":512,"diskSizeMB":3072,"state":"running","metadata":{"purpose":"test"},"envdAccessToken":"do-not-export-token","trafficAccessToken":"do-not-export-traffic","network":{"allowOut":["example.com"],"rules":{"example.com":[{"value":"do-not-export-rule"}]},"egressProxy":{"password":"do-not-export-proxy"}}}`)
	var c sandboxConfig
	if e := json.Unmarshal(raw, &c); e != nil {
		t.Fatal(e)
	}
	data, e := makeRecoveryMetadata(c, "runtime1", "snap1", childID)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), "do-not-export") {
		t.Fatal("credential entered metadata")
	}
	if !strings.Contains(string(data), "credential-bearing") || !strings.Contains(string(data), "purpose") {
		t.Fatal("missing metadata or unsupported-field declaration")
	}
	changed := c
	changed.MemoryMB = 1024
	if configurationStable(c, changed) {
		t.Fatal("configuration change not detected")
	}
	var decoded recoveryMetadata
	if e = json.Unmarshal(data, &decoded); e != nil {
		t.Fatal(e)
	}
	if decoded.BuildID != childID || decoded.Sandbox.Metadata["purpose"] != "test" {
		t.Fatal("metadata changed")
	}

}
func TestMetadataBackupLive(t *testing.T) {
	if os.Getenv("METADATA_LIVE") != "1" {
		t.Skip("set METADATA_LIVE=1 on runtime1")
	}
	c, e := configInit()
	if e != nil {
		t.Fatal(e)
	}
	w := worker{cfg: c, api: newAPI(c), s3: s3.NewFromConfig(aws.Config{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AK, c.SK, "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.Endpoint); o.UsePathStyle = true })}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	var sb sandbox
	if _, e = w.api.call(ctx, "POST", "/v2/sandboxes", map[string]any{"templateID": "base", "timeout": 600, "metadata": map[string]string{"purpose": "metadata-backup-validation"}}, &sb); e != nil {
		t.Fatal(e)
	}
	defer func() {
		clean, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		if _, e := w.api.call(clean, "DELETE", "/sandboxes/"+sb.ID, nil, nil); e != nil {
			t.Error(e)
		}
	}()
	if e = w.backup(ctx, sb.ID); e != nil {
		t.Fatal(e)
	}
	files, e := flowObjects(ctx, &w, sb.ID)
	if e != nil || len(files) == 0 {
		t.Fatal("missing uploaded backup", e)
	}
	key := files[len(files)-1].Key
	metaObject, e := w.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(metadataKey(key))})
	if e != nil {
		t.Fatal(e)
	}
	metadata, e := io.ReadAll(io.LimitReader(metaObject.Body, 1<<20))
	metaObject.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	var m recoveryMetadata
	if e = json.Unmarshal(metadata, &m); e != nil {
		t.Fatal(e)
	}
	if m.ArchiveKey != key || m.ArchiveBytes != files[len(files)-1].Bytes || m.ArchiveSHA256 == "" || m.BuildID == "" || m.Sandbox.SandboxID != sb.ID || m.Sandbox.CPUCount != 2 || m.Sandbox.MemoryMB != 512 || m.Sandbox.Metadata["purpose"] != "metadata-backup-validation" || m.SourceNode != c.Node {
		t.Fatal("uploaded metadata mismatch")
	}
	if bytes.Contains(metadata, []byte(c.Key)) || (sb.AccessToken != "" && bytes.Contains(metadata, []byte(sb.AccessToken))) {
		t.Fatal("access credential in metadata")
	}
	got, e := w.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Bucket), Key: aws.String(key)})
	if e != nil {
		t.Fatal(e)
	}
	defer got.Body.Close()
	if got.Metadata["sha256"] != m.ArchiveSHA256 {
		t.Fatal("metadata archive checksum binding mismatch")
	}
	gz, e := gzip.NewReader(got.Body)
	if e != nil {
		t.Fatal(e)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if h.Name == "sandbox-config.json" {
			t.Fatal("metadata embedded in archive")
		}
	}
	t.Logf("separate metadata verified key=%s metadata_key=%s archive_bytes=%d metadata_bytes=%d sandbox=%s cpu=%d ram_mb=%d", key, metadataKey(key), files[len(files)-1].Bytes, len(metadata), sb.ID, m.Sandbox.CPUCount, m.Sandbox.MemoryMB)

	// Retain this test's S3 archive for inspection; delete only its live sandbox.
}
