package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestAdminAuthenticationAndSnapshotList(t *testing.T) {
	calls := 0
	mock := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		calls++
		rw.Header().Set("Content-Type", "application/xml")
		rw.Write([]byte(`<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>backups/runtime1/sb1/filesystem/20261010T083055.897395106Z-abc.tar.gz</Key><Size>49</Size><LastModified>2026-10-10T08:30:56Z</LastModified></Contents><Contents><Key>backups/runtime1/sb1/filesystem/20261010T082919.157654441Z-def.tar.gz</Key><Size>48</Size><LastModified>2026-10-10T08:29:20Z</LastModified></Contents><Contents><Key>backups/runtime1/layers/base.tar.gz</Key><Size>480000000</Size></Contents><Contents><Key>backups/runtime2/sb1/filesystem/20261010T083055.897395106Z-other.tar.gz</Key><Size>44</Size></Contents><Contents><Key>backups/runtime1/sb1/filesystem/20261010T083055.897395106Z-abc.metadata.json</Key><Size>800</Size></Contents></ListBucketResult>`))
	}))
	defer mock.Close()
	w := worker{cfg: config{Node: "runtime1", Key: "test-team-key", Bucket: "test"}, s3: s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(mock.URL); o.UsePathStyle = true })}
	handler := w.adminHandler()
	for _, url := range []string{"/snapshots", "/snapshots?sandboxId=../other"} {
		r := httptest.NewRequest("GET", url, nil)
		rw := httptest.NewRecorder()
		handler.ServeHTTP(rw, r)
		if rw.Code != 401 {
			t.Fatal(rw.Code)
		}
	}
	if calls != 0 {
		t.Fatal("unauthenticated request reached S3")
	}
	r := httptest.NewRequest("GET", "/snapshots?sandboxId=../other", nil)
	r.Header.Set("X-API-Key", w.cfg.Key)
	rw := httptest.NewRecorder()
	handler.ServeHTTP(rw, r)
	if rw.Code != 400 {
		t.Fatal(rw.Code)
	}
	r = httptest.NewRequest("GET", "/snapshots?sandboxId=sb1", nil).WithContext(context.Background())
	r.Header.Set("X-API-Key", w.cfg.Key)
	rw = httptest.NewRecorder()
	handler.ServeHTTP(rw, r)
	if rw.Code != 200 {
		t.Fatal(rw.Code, rw.Body.String())
	}
	var got struct {
		Snapshots []adminSnapshot
		Count     int
	}
	if e := json.Unmarshal(rw.Body.Bytes(), &got); e != nil {
		t.Fatal(e)
	}
	if got.Count != 2 || got.Snapshots[0].Bytes != 49 || got.Snapshots[0].MetadataKey == "" || got.Snapshots[1].MetadataKey != "" || got.Snapshots[0].BackupAt <= got.Snapshots[1].BackupAt {
		t.Fatal(got)
	}
	if strings.Contains(rw.Body.String(), w.cfg.Key) {
		t.Fatal("credential exposed")
	}
	r = httptest.NewRequest("GET", "/snapshots", nil)
	r.Header.Set("X-API-Key", w.cfg.Key)
	rw = httptest.NewRecorder()
	handler.ServeHTTP(rw, r)
	var counts struct{ Counts map[string]int }
	if e := json.Unmarshal(rw.Body.Bytes(), &counts); e != nil {
		t.Fatal(e)
	}
	if counts.Counts["sb1"] != 2 || len(counts.Counts) != 1 {
		t.Fatal(counts)
	}
}

func TestAdminMetadataViewer(t *testing.T) {
	key := "backups/runtime1/sb1/filesystem/20261010T090000.000000000Z-" + childID + ".tar.gz"
	raw, _ := json.Marshal(recoveryMetadata{SchemaVersion: 1, SourceNode: "runtime1", ArchiveKey: key, BuildID: childID, Sandbox: sandboxConfig{SandboxID: "sb1", CPUCount: 2}})
	w := worker{cfg: config{Node: "runtime1", Key: "test-key", Bucket: "test"}, s3: fixtureS3(t, map[string][]byte{"/test/" + metadataKey(key): raw})}
	for _, test := range []struct {
		path, key string
		status    int
	}{
		{"/metadata?sandboxId=sb1&archiveKey=" + key, "", 401},
		{"/metadata?sandboxId=sb2&archiveKey=" + key, "test-key", 400},
		{"/metadata?sandboxId=sb1&archiveKey=" + strings.Replace(key, "runtime1", "runtime2", 1), "test-key", 400},
		{"/metadata?sandboxId=sb1&archiveKey=" + key, "test-key", 200},
		{"/metadata?sandboxId=sb1&archiveKey=" + strings.Replace(key, childID, baseID, 1), "test-key", 404},
	} {
		r := httptest.NewRequest("GET", test.path, nil)
		r.Header.Set("X-API-Key", test.key)
		rw := httptest.NewRecorder()
		w.adminHandler().ServeHTTP(rw, r)
		if rw.Code != test.status {
			t.Fatalf("want %d got %d: %s", test.status, rw.Code, rw.Body.String())
		}
		if test.status == 200 && !strings.Contains(rw.Body.String(), `"cpuCount":2`) {
			t.Fatal("missing JSON configuration")
		}
	}
}
