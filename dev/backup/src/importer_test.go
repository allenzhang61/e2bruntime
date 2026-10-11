package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func fixtureS3(t *testing.T, objects map[string][]byte) *s3.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, ok := objects[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		hash := sha256.Sum256(data)
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.Header().Set("X-Amz-Meta-Sha256", hex.EncodeToString(hash[:]))
		w.Header().Set("Last-Modified", "Sat, 10 Oct 2026 07:00:00 GMT")
		w.Write(data)
	}))
	t.Cleanup(server.Close)
	return s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
}
func TestImportExternalDependencies(t *testing.T) {
	for _, prefix := range []string{"backups/runtime1/", "backups/workspace-abc123/runtime1/"} {
		t.Run(prefix, func(t *testing.T) { testImportExternalDependencies(t, prefix, prefix+"layers/") })
	}
	t.Run("global-shared", func(t *testing.T) {
		testImportExternalDependencies(t, "backups/workspace-abc123/runtime1/", sharedLayerPrefix("runtime1"))
	})
}
func testImportExternalDependencies(t *testing.T, prefix string, layerPrefix string) {
	root := fixture(t)
	os.Remove(filepath.Join(root, childID, "memfile.header"))
	parentKey := layerPrefix + baseID + ".tar.gz"
	childKey := prefix + "demo/filesystem/20261010T070000.000000000Z-" + childID + ".tar.gz"
	var base, child bytes.Buffer
	parent := Exporter{StorageRoot: root, AllowMemoryHeader: true}
	if _, e := parent.ExportBuild(context.Background(), Record{baseID, "filesystem"}, &base); e != nil {
		t.Fatal(e)
	}
	ex := Exporter{StorageRoot: root, ExternalBuild: func(context.Context, string) (string, error) { return parentKey, nil }}
	if _, e := ex.ExportBuild(context.Background(), Record{childID, "filesystem"}, &child); e != nil {
		t.Fatal(e)
	}
	im := importer{client: fixtureS3(t, map[string][]byte{"/test/" + parentKey: base.Bytes(), "/test/" + childKey: child.Bytes()}), bucket: "test", stage: t.TempDir(), seen: map[string]bool{}}
	manifest, e := im.download(context.Background(), childKey)
	if e != nil {
		t.Fatal(e)
	}
	if manifest.Record.BuildID != childID || len(im.files) != 2 {
		t.Fatal("missing dependency")
	}
	data, e := os.ReadFile(filepath.Join(im.stage, baseID, "rootfs.ext4"))
	if e != nil || string(data) != "data" {
		t.Fatal("base data not imported")
	}
}
func TestImportRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0600, Size: 1})
	tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	key := "backups/runtime1/demo/filesystem/bad.tar.gz"
	stage := t.TempDir()
	im := importer{client: fixtureS3(t, map[string][]byte{"/test/" + key: buf.Bytes()}), bucket: "test", stage: stage, seen: map[string]bool{}}
	if _, e := im.download(context.Background(), key); e == nil {
		t.Fatal("accepted unsafe archive path")
	}
}

func TestSharedLayerScope(t *testing.T) {
	i := importer{sourceNode: "runtime1"}
	for _, tc := range []struct {
		key   string
		valid bool
	}{
		{sharedLayerPrefix("runtime1") + baseID + ".tar.gz", true},
		{sharedLayerPrefix("runtime2") + baseID + ".tar.gz", false},
		{sharedLayerPrefix("runtime1") + "../" + baseID + ".tar.gz", false},
		{sharedLayerPrefix("runtime1") + "not-uuid.tar.gz", false},
		{"backups/workspace-abc123/runtime1/layers/" + baseID + ".tar.gz", false},
	} {
		if i.validSharedLayerKey(tc.key) != tc.valid {
			t.Errorf("invalid scope for %s", tc.key)
		}
	}
}
