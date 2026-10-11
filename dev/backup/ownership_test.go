package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func ownerFixture(t *testing.T) *s3.Client {
	t.Helper()
	objects := map[string][]byte{}
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		data, exists := objects[r.URL.Path]
		hash := sha256.Sum256(data)
		etag := `"` + hex.EncodeToString(hash[:]) + `"`
		if r.Method == "PUT" {
			if (r.Header.Get("If-None-Match") == "*" && exists) || (r.Header.Get("If-Match") != "" && (!exists || r.Header.Get("If-Match") != etag)) {
				w.WriteHeader(412)
				fmt.Fprint(w, "<Error><Code>PreconditionFailed</Code></Error>")
				return
			}
			data, _ = io.ReadAll(r.Body)
			objects[r.URL.Path] = data
			hash = sha256.Sum256(data)
			w.Header().Set("ETag", `"`+hex.EncodeToString(hash[:])+`"`)
			return
		}
		if !exists {
			w.WriteHeader(404)
			fmt.Fprint(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.Write(data)
	}))
	t.Cleanup(server.Close)
	return s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
}
func TestOwnershipHandoverAndStrictImport(t *testing.T) {
	ctx := context.Background()
	source := worker{cfg: config{Node: "runtime1", Bucket: "test"}, s3: ownerFixture(t)}
	target := worker{cfg: config{Node: "runtime2", Bucket: "test"}, s3: source.s3}
	c := sandboxConfig{SandboxID: "sb1"}
	owner, etag, err := source.backupPermission(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	e := transferEntry{SourceNode: "runtime1", SandboxID: "sb1", ArchiveKey: "backups/runtime1/sb1/filesystem/20261010T090000.000000000Z-" + childID + ".tar.gz", Metadata: &recoveryMetadata{Sandbox: c}}
	if _, _, err = target.authorizeImport(ctx, e); err == nil {
		t.Fatal("target imported source-owned backup")
	}
	owner.OwnerNode = "runtime2"
	owner.OwnerSandboxID = ""
	owner.State = "ready"
	owner.PinnedArchive = e.ArchiveKey
	owner.Generation++
	next, err := source.writeOwner(ctx, owner, etag)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.writeOwner(ctx, owner, etag); err == nil {
		t.Fatal("stale ETag overwrote owner")
	}
	if _, _, err = source.backupPermission(ctx, c); err == nil {
		t.Fatal("old Runtime backed up after handover")
	}
	if _, _, err = source.authorizeImport(ctx, e); err == nil {
		t.Fatal("source imported target-owned backup")
	}
	got, current, err := target.authorizeImport(ctx, e)
	if err != nil || current != next {
		t.Fatal(err)
	}
	got.State = "importing"
	got.Generation++
	if _, err = target.writeOwner(ctx, got, current); err != nil {
		t.Fatal(err)
	}
	if _, _, err = target.authorizeImport(ctx, e); err == nil {
		t.Fatal("duplicate import accepted")
	}
	e.SandboxID = "missing"
	e.Metadata = &recoveryMetadata{Sandbox: sandboxConfig{SandboxID: "missing"}}
	if _, _, err = target.authorizeImport(ctx, e); err == nil {
		t.Fatal("missing ownership imported")
	}
}
func TestTransferValidationAndReadOnlyExport(t *testing.T) {
	archive := "backups/runtime1/sb1/filesystem/20261010T090000.000000000Z-" + childID + ".tar.gz"
	m := recoveryMetadata{SchemaVersion: 1, SourceNode: "runtime1", ArchiveKey: archive, ArchiveBytes: 7, ArchiveSHA256: strings.Repeat("a", 64), BuildID: childID, Sandbox: sandboxConfig{SandboxID: "sb1"}}
	e := transferEntry{SchemaVersion: 1, SourceNode: "runtime1", SandboxID: "sb1", ArchiveKey: archive, MetadataKey: metadataKey(archive), Bucket: "test", Importable: true, Metadata: &m}
	w := worker{cfg: config{Node: "runtime2", Bucket: "test", ImportSources: "runtime1"}}
	if err := w.validateTransfer(e); err != nil {
		t.Fatal(err)
	}
	e.Bucket = "elsewhere"
	if w.validateTransfer(e) == nil {
		t.Fatal("different bucket accepted")
	}
	e.Bucket = "test"
	e.SourceNode = "runtime3"
	if w.validateTransfer(e) == nil {
		t.Fatal("wrong source accepted")
	}
	data, _ := json.Marshal(m)
	sum := sha256.Sum256(data)
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			writes++
			rw.WriteHeader(500)
			return
		}
		if r.URL.Query().Has("list-type") {
			rw.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(rw, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>%s</Key><Size>7</Size></Contents><Contents><Key>%s</Key><Size>6</Size></Contents></ListBucketResult>`, archive, strings.Replace(archive, "090000", "080000", 1))
			return
		}
		if strings.HasSuffix(r.URL.Path, "metadata.json") {
			rw.Header().Set("Content-Length", fmt.Sprint(len(data)))
			rw.Header().Set("X-Amz-Meta-Sha256", hex.EncodeToString(sum[:]))
			rw.Write(data)
			return
		}
		rw.Header().Set("Content-Length", "7")
		rw.Header().Set("X-Amz-Meta-Sha256", m.ArchiveSHA256)
	}))
	defer server.Close()
	w.cfg.Node = "runtime1"
	w.s3 = s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
	entries, err := w.exportLatest(context.Background())
	if err != nil || len(entries) != 1 || entries[0].ArchiveKey != archive || !entries[0].Importable || writes != 0 {
		t.Fatal(entries, err, writes)
	}
}

func TestMetadataComparisonIgnoresJSONFormatting(t *testing.T) {
	original := recoveryMetadata{Sandbox: sandboxConfig{Lifecycle: json.RawMessage(`{"autoResume": false, "onTimeout": "kill"}`)}}
	pasted := original
	pasted.Sandbox.Lifecycle = json.RawMessage(`{ "onTimeout": "kill", "autoResume": false }`)
	if !metadataEqual(original, &pasted) {
		t.Fatal("equivalent formatted JSON rejected")
	}
	pasted.Sandbox.CPUCount = 9
	if metadataEqual(original, &pasted) {
		t.Fatal("changed configuration accepted")
	}
}
