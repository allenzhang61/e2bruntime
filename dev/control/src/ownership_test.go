package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestSwitchOnlyUpdatesS3AndRestoreUsesOwner(t *testing.T) {
	archive := "backups/runtime1/original/filesystem/20261010T100000.000000000Z-123.tar.gz"
	mk := strings.TrimSuffix(archive, ".tar.gz") + ".metadata.json"
	metadata := fmt.Sprintf(`{"schema_version":1,"source_node":"runtime1","archive_key":%q,"archive_bytes":100,"captured_at_utc":"2026-10-10T10:00:00Z","sandbox":{"sandboxID":"original","metadata":{}},"external_build_archives":{}}`, archive)
	hash := sha256.Sum256([]byte(metadata))
	record := []byte(`{"schema_version":1,"logical_node":"runtime1","logical_sandbox_id":"original","owner_node":"runtime1","owner_sandbox_id":"original","state":"active","generation":1}`)
	var mu sync.Mutex
	var puts int
	rejectWrite := false
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/control/workspaces.json"):
			w.Header().Set("ETag", `"registry"`)
			fmt.Fprint(w, `{"schema_version":1,"ids":{"runtime1/original":"workspace-abc123"}}`)
		case r.URL.Query().Has("list-type"):
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>%s</Key><Size>100</Size></Contents><Contents><Key>%s</Key><Size>%d</Size></Contents></ListBucketResult>`, archive, mk, len(metadata))
		case strings.HasSuffix(r.URL.Path, ".metadata.json"):
			w.Header().Set("X-Amz-Meta-Sha256", hex.EncodeToString(hash[:]))
			fmt.Fprint(w, metadata)
		default:
			if r.Method == "PUT" {
				if r.Header.Get("If-Match") != `"owner"` || rejectWrite {
					w.WriteHeader(412)
					fmt.Fprint(w, `<Error><Code>PreconditionFailed</Code></Error>`)
					return
				}
				puts++
				record, _ = io.ReadAll(r.Body)
			}
			w.Header().Set("ETag", `"owner"`)
			if r.Method != "PUT" {
				w.Write(record)
			}
		}
	}))
	defer store.Close()
	var mutations atomic.Int32
	rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations.Add(1)
			w.WriteHeader(500)
			return
		}
		// Destination is offline: changing S3 ownership must still work.
		w.WriteHeader(503)
	}))
	defer rt.Close()
	s := server{cfg: config{Bucket: "test", Data: t.TempDir(), Runtimes: []runtimeConfig{{Name: "runtime1", API: rt.URL, Admin: rt.URL}, {Name: "runtime2", API: rt.URL, Admin: rt.URL}}}, http: http.DefaultClient, jobs: []job{}, s3: s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(store.URL); o.UsePathStyle = true })}
	router := s.handler()
	send := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost:13021"+path, strings.NewReader(body))
		r.Header.Set("X-Control-Request", "1")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, r)
		return out
	}
	out := send("/api/switch", `{"logic_id":"workspace-abc123","target_runtime":"runtime2"}`)
	if out.Code != 200 {
		t.Fatalf("switch failed: %d %s", out.Code, out.Body.String())
	}
	mu.Lock()
	var saved owner
	err := json.Unmarshal(record, &saved)
	n := puts
	mu.Unlock()
	if err != nil || saved.Node != "runtime2" || saved.Instance != "" || saved.State != "ready" || saved.Generation != 2 || saved.Archive != archive || n != 1 || mutations.Load() != 0 || len(s.jobs) != 0 || s.busy {
		t.Fatalf("switch unexpectedly restored or invalid owner: %+v", saved)
	}
	// No selectable target on restore: it must use the saved runtime2 owner.
	out = send("/api/restore", `{"logic_id":"workspace-abc123","target_runtime":"runtime1"}`)
	if out.Code != 400 {
		t.Fatalf("restore accepted target override: %d", out.Code)
	}
	out = send("/api/restore", `{"logic_id":"workspace-abc123"}`)
	if out.Code != 409 || !strings.Contains(out.Body.String(), "owner Runtime unavailable") || mutations.Load() != 0 {
		t.Fatalf("restore did not respect owner: %d %s", out.Code, out.Body.String())
	}
	mu.Lock()
	rejectWrite = true
	mu.Unlock()
	_, err = s.assignOwner(context.Background(), logic{OriginNode: "runtime1", OriginID: "original"}, "runtime1")
	if err == nil {
		t.Fatal("concurrent S3 update was overwritten")
	}
	mu.Lock()
	defer mu.Unlock()
	if puts != 1 {
		t.Fatal("failed CAS mutated ownership")
	}
}
