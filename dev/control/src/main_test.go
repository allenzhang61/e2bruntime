package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInventorySurvivesSourceOutageAndRejectsDuplicate(t *testing.T) {
	archive := "backups/runtime1/original/filesystem/20261010T100000.000000000Z-123.tar.gz"
	mk := strings.TrimSuffix(archive, ".tar.gz") + ".metadata.json"
	raw := fmt.Sprintf(`{"schema_version":1,"source_node":"runtime1","archive_key":%q,"archive_bytes":100,"captured_at_utc":"2026-10-10T10:00:00Z","sandbox":{"sandboxID":"original","metadata":{}},"external_build_archives":{}}`, archive)
	hash := sha256.Sum256([]byte(raw))
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/control/workspaces.json"):
			w.Header().Set("ETag", "\"registry\"")
			fmt.Fprint(w, `{"schema_version":1,"ids":{"runtime1/original":"workspace-abc123"}}`)
		case r.URL.Query().Has("list-type"):
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>%s</Key><Size>100</Size></Contents><Contents><Key>%s</Key><Size>%d</Size></Contents></ListBucketResult>`, archive, mk, len(raw))
		case strings.HasSuffix(r.URL.Path, ".metadata.json"):
			w.Header().Set("X-Amz-Meta-Sha256", hex.EncodeToString(hash[:]))
			fmt.Fprint(w, raw)
		default:
			fmt.Fprint(w, `{"owner_node":"runtime2","owner_sandbox_id":"restored","state":"active","generation":2}`)
		}
	}))
	defer store.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer dead.Close()
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"sandboxID":"restored","state":"paused","metadata":{"dev_backup_origin_node":"runtime1","dev_backup_origin_id":"original"}}]`)
	}))
	defer live.Close()
	s := server{cfg: config{Bucket: "test", Data: t.TempDir(), Runtimes: []runtimeConfig{{Name: "runtime1", API: dead.URL}, {Name: "runtime2", API: live.URL}}}, http: http.DefaultClient, s3: s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(store.URL); o.UsePathStyle = true })}
	v, e := s.inventory(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if len(v.Logics) != 1 || v.Logics[0].ID != "workspace-abc123" || len(v.Logics[0].Images) != 1 || len(v.Logics[0].Instances) != 1 || v.Errors["runtime1"] == "" {
		t.Fatalf("bad inventory %+v", v)
	}
	req := httptest.NewRequest("POST", "http://localhost:13021/api/restore", strings.NewReader(`{"logic_id":"workspace-abc123"}`))
	req.Header.Set("Origin", "http://localhost:13021")
	out := httptest.NewRecorder()
	s.handler().ServeHTTP(out, req)
	if out.Code != 409 || !strings.Contains(out.Body.String(), "already exists") || len(s.jobs) != 0 || s.busy {
		t.Fatalf("duplicate not rejected: %d %s", out.Code, out.Body.String())
	}
}
func TestMutationsRejectForeignOrigin(t *testing.T) {
	s := server{}
	req := httptest.NewRequest("POST", "http://localhost:13021/api/switch", nil)
	req.Header.Set("Origin", "http://evil.example")
	out := httptest.NewRecorder()
	s.handler().ServeHTTP(out, req)
	if out.Code != 403 {
		t.Fatalf("got %d", out.Code)
	}
}
