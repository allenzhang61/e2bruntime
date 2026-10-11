package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type restoreFile struct {
	Key      string `json:"key"`
	Bytes    int64  `json:"bytes"`
	StoredAt string `json:"stored_at_utc"`
	BackupAt string `json:"backup_at_utc,omitempty"`
	SHA256   string `json:"sha256"`
}
type importer struct {
	client        *s3.Client
	bucket, stage string
	sourceNode    string
	sourcePrefix  string
	seen          map[string]bool
	files         []restoreFile
}

func (i *importer) download(ctx context.Context, key string) (Manifest, error) {
	var manifest Manifest
	if i.sourceNode == "" {
		i.sourceNode = "runtime1"
	}
	if i.seen[key] {
		return manifest, fmt.Errorf("repeated dependency archive")
	}
	i.seen[key] = true
	if i.sourcePrefix == "" {
		parts := strings.Split(key, "/")
		i.sourcePrefix = "backups/" + i.sourceNode + "/"
		if len(parts) > 2 && workspacePattern.MatchString(parts[1]) {
			i.sourcePrefix = "backups/" + parts[1] + "/" + i.sourceNode + "/"
		}
	}
	if len(i.seen) > 1024 || (!strings.HasPrefix(key, i.sourcePrefix) && !i.validSharedLayerKey(key)) || strings.Contains(key, "..") {
		return manifest, fmt.Errorf("unsafe dependency key")
	}
	got, e := i.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(i.bucket), Key: aws.String(key)})
	if e != nil {
		return manifest, fmt.Errorf("GET failed (%T)", e)
	}
	defer got.Body.Close()
	f, e := os.CreateTemp(i.stage, "download-*.tar.gz")
	if e != nil {
		return manifest, e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, hash), io.LimitReader(got.Body, (5<<30)+1))
	if e != nil {
		return manifest, e
	}
	sha := hex.EncodeToString(hash.Sum(nil))
	if n > 5<<30 || n != aws.ToInt64(got.ContentLength) || sha != got.Metadata["sha256"] {
		return manifest, fmt.Errorf("archive checksum/size mismatch")
	}
	if _, e = f.Seek(0, 0); e != nil {
		return manifest, e
	}
	gz, e := gzip.NewReader(f)
	if e != nil {
		return manifest, e
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	actual := map[string]File{}
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return manifest, e
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return manifest, fmt.Errorf("only regular archive entries allowed")
		}
		if h.Size < 0 || h.Size > 16<<30 {
			return manifest, fmt.Errorf("entry too large")
		}
		if h.Name == "manifest.json" {
			if h.Size > 4<<20 {
				return manifest, fmt.Errorf("manifest too large")
			}
			if e = json.NewDecoder(io.LimitReader(tr, h.Size)).Decode(&manifest); e != nil {
				return manifest, e
			}
			continue
		}
		if h.Name == "sandbox-config.json" {
			if h.Size > 1<<20 {
				return manifest, fmt.Errorf("sandbox configuration too large")
			}
			if _, duplicate := actual[h.Name]; duplicate {
				return manifest, fmt.Errorf("duplicate sandbox configuration")
			}
			data, err := io.ReadAll(io.LimitReader(tr, h.Size))
			if err != nil || int64(len(data)) != h.Size || !json.Valid(data) {
				return manifest, fmt.Errorf("invalid sandbox configuration")
			}
			if err = os.WriteFile(filepath.Join(i.stage, h.Name), data, 0600); err != nil {
				return manifest, err
			}
			hash := sha256.Sum256(data)
			actual[h.Name] = File{h.Name, h.Size, hex.EncodeToString(hash[:])}
			continue
		}
		parts := strings.Split(h.Name, "/")
		if len(parts) != 3 || parts[0] != "templates" || !uuidPattern.MatchString(parts[1]) {
			return manifest, fmt.Errorf("unsafe archive path")
		}
		allowed := parts[2] == "metadata.json" || parts[2] == "rootfs.ext4" || parts[2] == "rootfs.ext4.header"
		if !allowed {
			return manifest, fmt.Errorf("unexpected filesystem artifact")
		}
		name := parts[1] + "/" + parts[2]
		if _, ok := actual[name]; ok {
			return manifest, fmt.Errorf("duplicate archive entry")
		}
		dir := filepath.Join(i.stage, parts[1])
		if e = os.MkdirAll(dir, 0700); e != nil {
			return manifest, e
		}
		out, e := os.OpenFile(filepath.Join(dir, parts[2]), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return manifest, e
		}
		hsh := sha256.New()
		size, copyErr := io.Copy(io.MultiWriter(out, hsh), tr)
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil {
			return manifest, fmt.Errorf("extract failed")
		}
		actual[name] = File{name, size, hex.EncodeToString(hsh.Sum(nil))}
	}
	if manifest.Format != "e2b-embed-native-v3-tar-gzip-v1" && manifest.Format != "e2b-embed-native-v3-tar-gzip-v2" {
		return manifest, fmt.Errorf("unknown archive format")
	}
	if manifest.Record.Kind != "filesystem" || !uuidPattern.MatchString(manifest.Record.BuildID) {
		return manifest, fmt.Errorf("not a filesystem archive")
	}
	if len(actual) != len(manifest.Files) {
		return manifest, fmt.Errorf("manifest file count mismatch")
	}
	for _, expected := range manifest.Files {
		if actual[expected.Name] != expected {
			return manifest, fmt.Errorf("file hash mismatch")
		}
	}
	record := restoreFile{Key: key, Bytes: n, StoredAt: aws.ToTime(got.LastModified).UTC().Format(time.RFC3339), SHA256: sha}
	base := filepath.Base(key)
	if strings.Contains(key, "/filesystem/") {
		stamp := strings.SplitN(base, "-", 2)[0]
		if ts, err := time.Parse("20060102T150405.000000000Z", stamp); err == nil {
			record.BackupAt = ts.Format(time.RFC3339Nano)
		}
	}
	i.files = append(i.files, record)
	for id, dependency := range manifest.External {
		if !uuidPattern.MatchString(id) || (dependency != i.sourcePrefix+"layers/"+id+".tar.gz" && dependency != sharedLayerPrefix(i.sourceNode)+id+".tar.gz") {
			return manifest, fmt.Errorf("invalid external dependency")
		}
		if i.seen[dependency] {
			continue
		}
		parent, e := i.download(ctx, dependency)
		if e != nil {
			return manifest, e
		}
		if parent.Record.BuildID != id {
			return manifest, fmt.Errorf("dependency build mismatch")
		}
	}
	return manifest, nil
}

func (i *importer) validSharedLayerKey(key string) bool {
	prefix := sharedLayerPrefix(i.sourceNode)
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, ".tar.gz") {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(key, prefix), ".tar.gz")
	return uuidPattern.MatchString(id)
}
