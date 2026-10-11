// Export supports immutable V3 snapshot layers in a self-hosted Embed storage
// directory. It does not export team credentials or restore database rows.
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var names = []string{"metadata.json", "memfile.header", "memfile", "snapfile", "rootfs.ext4.header", "rootfs.ext4"}

type Record struct {
	BuildID string `json:"build_id"`
	Kind    string `json:"kind"`
}

type Exporter struct {
	// StorageRoot is the TEMPLATE_STORAGE_URL file:// directory on the node.
	StorageRoot       string
	AllowMemoryHeader bool
	ExternalBuild     func(context.Context, string) (string, error)
	// Resolve must authorize the handle, select its completed ready build and
	// confirm its kind. Hibernate handles are sandbox IDs, not storage build IDs.
	Resolve func(context.Context, string, string) (Record, error)
}

type File struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Format   string            `json:"format"`
	Record   Record            `json:"record"`
	Builds   []string          `json:"builds"`
	Files    []File            `json:"files"`
	Scope    string            `json:"scope"`
	External map[string]string `json:"external_build_archives,omitempty"`
}

// ExportBuild preserves E2B's native layers and headers. Consumers still need
// compatible runtime/kernel versions and control-plane snapshot registration.
func (e *Exporter) ExportBuild(ctx context.Context, record Record, output io.Writer) (manifest Manifest, err error) {
	if !filepath.IsAbs(e.StorageRoot) || !uuidPattern.MatchString(record.BuildID) {
		return manifest, errors.New("absolute storage root and UUID build ID required")
	}
	if record.Kind != "hibernate" && record.Kind != "filesystem" {
		return manifest, errors.New("invalid artifact kind")
	}
	root, err := os.OpenRoot(e.StorageRoot)
	if err != nil {
		return manifest, err
	}
	defer root.Close()
	seen := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if seen[id] {
			return nil
		}
		if len(seen) >= 1024 {
			return errors.New("too many dependency builds")
		}
		st, err := root.Lstat(id)
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("build must be a real directory")
		}
		seen[id] = true
		kinds := []string{"rootfs.ext4"}
		if record.Kind == "hibernate" {
			kinds = append(kinds, "memfile")
		}
		for _, kind := range kinds {
			file := id + "/" + kind + ".header"
			f, er := root.Open(file)
			if os.IsNotExist(er) && kind == "memfile" {
				continue
			}
			if er != nil {
				return er
			}
			data, er := io.ReadAll(io.LimitReader(f, 16<<20))
			f.Close()
			if er != nil {
				return er
			}
			if len(data) < 64 || binary.LittleEndian.Uint64(data[:8]) != 3 || (len(data)-64)%40 != 0 {
				return fmt.Errorf("%s: only complete V3 headers are supported", file)
			}
			if uuidString(data[32:48]) != id {
				return errors.New("header build ID mismatch")
			}
			base := uuidString(data[48:64])
			if base != zeroUUID {
				if er = visit(base); er != nil {
					return er
				}
			}
			for off := 64; off < len(data); off += 40 {
				source := uuidString(data[off+16 : off+32])
				if source == zeroUUID {
					continue
				}
				length, offset := binary.LittleEndian.Uint64(data[off+8:off+16]), binary.LittleEndian.Uint64(data[off+32:off+40])
				st, er := root.Lstat(source + "/" + kind)
				if er != nil {
					return fmt.Errorf("missing dependency data %s/%s: %w", source, kind, er)
				}
				if !st.Mode().IsRegular() || offset > uint64(st.Size()) || length > uint64(st.Size())-offset {
					return errors.New("dependency data does not cover header mapping")
				}
				if er = visit(source); er != nil {
					return er
				}
			}
		}
		return nil
	}
	if err = visit(record.BuildID); err != nil {
		return manifest, err
	}
	_, memErr := root.Stat(record.BuildID + "/memfile.header")
	if (record.Kind == "hibernate" && memErr != nil) || (record.Kind == "filesystem" && !e.AllowMemoryHeader && !os.IsNotExist(memErr)) {
		return manifest, errors.New("snapshot kind does not match memory header")
	}
	if record.Kind == "hibernate" {
		if st, er := root.Lstat(record.BuildID + "/snapfile"); er != nil || !st.Mode().IsRegular() {
			return manifest, errors.New("full snapshot requires snapfile")
		}
	}
	manifest = Manifest{Format: "e2b-embed-native-v3-tar-gzip-v1", Record: record, Scope: "native storage layers only; database registration and runtime binaries are external prerequisites"}
	for id := range seen {
		manifest.Builds = append(manifest.Builds, id)
	}
	sort.Strings(manifest.Builds)
	if e.ExternalBuild != nil {
		manifest.Format = "e2b-embed-native-v3-tar-gzip-v2"
		manifest.External = map[string]string{}
		for _, id := range manifest.Builds {
			if id == record.BuildID {
				continue
			}
			key, er := e.ExternalBuild(ctx, id)
			if er != nil {
				return manifest, er
			}
			manifest.External[id] = key
		}
	}
	gz := gzip.NewWriter(output)
	tw := tar.NewWriter(gz)
	defer func() { err = errors.Join(err, tw.Close(), gz.Close()) }()
	for _, id := range manifest.Builds {
		if manifest.External[id] != "" {
			continue
		}
		for _, name := range names {
			if err = ctx.Err(); err != nil {
				return manifest, err
			}
			if record.Kind == "filesystem" && (strings.HasPrefix(name, "memfile") || name == "snapfile") {
				continue
			}
			p := id + "/" + name
			st, er := root.Lstat(p)
			if os.IsNotExist(er) {
				continue
			}
			if er != nil {
				return manifest, er
			}
			if !st.Mode().IsRegular() {
				return manifest, errors.New("only regular snapshot files allowed")
			}
			f, er := root.Open(p)
			if er != nil {
				return manifest, er
			}
			er = tw.WriteHeader(&tar.Header{Name: "templates/" + p, Mode: 0600, Size: st.Size()})
			hash := sha256.New()
			var n int64
			if er == nil {
				n, er = io.Copy(io.MultiWriter(tw, hash), &contextReader{ctx, f})
			}
			f.Close()
			if er != nil || n != st.Size() {
				return manifest, fmt.Errorf("copy %s: %w", p, errors.Join(er, io.ErrUnexpectedEOF))
			}
			manifest.Files = append(manifest.Files, File{p, n, hex.EncodeToString(hash.Sum(nil))})
		}
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, err
	}
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Size: int64(len(data))}); err != nil {
		return manifest, err
	}
	_, err = tw.Write(data)
	return
}

const zeroUUID = "00000000-0000-0000-0000-000000000000"

func uuidString(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
