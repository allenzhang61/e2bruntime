package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const baseID = "11111111-1111-4111-8111-111111111111"
const childID = "22222222-2222-4222-8222-222222222222"

func headerFixture(id, source string) []byte {
	b := make([]byte, 104)
	binary.LittleEndian.PutUint64(b, 3)
	binary.LittleEndian.PutUint64(b[8:], 4096)
	self, _ := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	ref, _ := hex.DecodeString(strings.ReplaceAll(source, "-", ""))
	copy(b[32:48], self)
	copy(b[48:64], ref)
	copy(b[80:96], ref)
	binary.LittleEndian.PutUint64(b[72:], 4)
	return b
}
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, id := range []string{baseID, childID} {
		dir := filepath.Join(root, id)
		os.Mkdir(dir, 0700)
		for _, name := range []string{"memfile.header", "rootfs.ext4.header"} {
			os.WriteFile(filepath.Join(dir, name), headerFixture(id, baseID), 0600)
		}
		os.WriteFile(filepath.Join(dir, "metadata.json"), []byte(`{}`), 0600)
		os.WriteFile(filepath.Join(dir, "snapfile"), []byte("state"), 0600)
	}
	for _, name := range []string{"memfile", "rootfs.ext4"} {
		os.WriteFile(filepath.Join(root, baseID, name), []byte("data"), 0600)
	}
	return root
}
func TestDependenciesAndKinds(t *testing.T) {
	root := fixture(t)
	e := Exporter{StorageRoot: root}
	var out bytes.Buffer
	manifest, err := e.ExportBuild(context.Background(), Record{childID, "hibernate"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Builds) != 2 {
		t.Fatal("parent build not exported")
	}
	gz, err := gzip.NewReader(&out)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		seen[h.Name], err = io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range manifest.Files {
		sum := sha256.Sum256(seen["templates/"+f.Name])
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Fatal("bad manifest hash")
		}
	}
	os.Remove(filepath.Join(root, childID, "memfile.header"))
	out.Reset()
	_, err = e.ExportBuild(context.Background(), Record{childID, "filesystem"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	gz, _ = gzip.NewReader(&out)
	defer gz.Close()
	tr = tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(h.Name, "memfile") || strings.HasSuffix(h.Name, "snapfile") {
			t.Fatal("filesystem archive leaked RAM")
		}
	}
}
func TestRejectIncompleteAndUnknown(t *testing.T) {
	root := fixture(t)
	e := Exporter{StorageRoot: root}
	os.Remove(filepath.Join(root, baseID, "memfile"))
	if _, err := e.ExportBuild(context.Background(), Record{childID, "hibernate"}, io.Discard); err == nil {
		t.Fatal("accepted missing dependency")
	}
	os.WriteFile(filepath.Join(root, baseID, "memfile"), []byte("data"), 0600)
	b := headerFixture(childID, baseID)
	binary.LittleEndian.PutUint64(b, 5)
	os.WriteFile(filepath.Join(root, childID, "memfile.header"), b, 0600)
	if _, err := e.ExportBuild(context.Background(), Record{childID, "hibernate"}, io.Discard); err == nil {
		t.Fatal("accepted unknown header")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.ExportBuild(ctx, Record{childID, "hibernate"}, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestExternalParentLayers(t *testing.T) {
	root := fixture(t)
	os.Remove(filepath.Join(root, childID, "memfile.header"))
	ex := Exporter{StorageRoot: root, ExternalBuild: func(ctx context.Context, id string) (string, error) { return "layers/" + id + ".tar.gz", nil }}
	var out bytes.Buffer
	manifest, err := ex.ExportBuild(context.Background(), Record{childID, "filesystem"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.External[baseID] != "layers/"+baseID+".tar.gz" {
		t.Fatal("missing external dependency")
	}
	for _, f := range manifest.Files {
		if strings.HasPrefix(f.Name, baseID+"/") {
			t.Fatal("base duplicated")
		}
	}
}
