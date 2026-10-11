package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type transferEntry struct {
	OwnershipGeneration int64             `json:"ownership_generation,omitempty"`
	SchemaVersion       int               `json:"schema_version"`
	SourceNode          string            `json:"source_node"`
	SandboxID           string            `json:"source_sandbox_id"`
	BackupAt            string            `json:"backup_at_utc"`
	Bucket              string            `json:"bucket"`
	ArchiveKey          string            `json:"archive_key"`
	MetadataKey         string            `json:"metadata_key"`
	Metadata            *recoveryMetadata `json:"metadata,omitempty"`
	Importable          bool              `json:"importable"`
	Problem             string            `json:"problem,omitempty"`
}

func archiveIdentity(key string) (node, id, build string, at time.Time, err error) {
	parts := backupParts(key)
	if len(parts) != 5 || parts[0] != "backups" || parts[3] != "filesystem" || !sandboxIDPattern.MatchString(parts[1]) || !sandboxIDPattern.MatchString(parts[2]) || strings.Contains(parts[1], ".") || !strings.HasSuffix(parts[4], ".tar.gz") {
		err = fmt.Errorf("invalid archive key")
		return
	}
	stamp, b, ok := strings.Cut(strings.TrimSuffix(parts[4], ".tar.gz"), "-")
	at, err = time.Parse("20060102T150405.000000000Z", stamp)
	if err != nil || !ok || !uuidPattern.MatchString(b) {
		err = fmt.Errorf("invalid archive timestamp/build")
		return
	}
	return parts[1], parts[2], b, at, nil
}
func (w *worker) readRecovery(ctx context.Context, key string) (recoveryMetadata, error) {
	var m recoveryMetadata
	node, id, build, _, err := archiveIdentity(key)
	if err != nil {
		return m, err
	}
	got, err := w.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(metadataKey(key))})
	if err != nil {
		return m, fmt.Errorf("Metadata object missing or inaccessible")
	}
	defer got.Body.Close()
	data, err := io.ReadAll(io.LimitReader(got.Body, (1<<20)+1))
	sum := sha256.Sum256(data)
	if err != nil || len(data) > 1<<20 || int64(len(data)) != aws.ToInt64(got.ContentLength) || hex.EncodeToString(sum[:]) != got.Metadata["sha256"] || json.Unmarshal(data, &m) != nil || m.SchemaVersion != 1 || m.SourceNode != node || m.Sandbox.SandboxID != id || m.ArchiveKey != key || m.BuildID != build || m.ArchiveBytes < 1 || len(m.ArchiveSHA256) != 64 {
		return m, fmt.Errorf("Metadata verification failed")
	}
	if _, err = hex.DecodeString(m.ArchiveSHA256); err != nil {
		return m, fmt.Errorf("invalid archive hash")
	}
	return m, nil
}
func (w *worker) exportLatest(ctx context.Context) ([]transferEntry, error) {
	latest := map[string]string{}
	p := s3.NewListObjectsV2Paginator(w.s3, &s3.ListObjectsV2Input{Bucket: aws.String(w.cfg.Bucket), Prefix: aws.String("backups/")})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("snapshot storage unavailable")
		}
		for _, o := range page.Contents {
			key := aws.ToString(o.Key)
			node, id, _, _, err := archiveIdentity(key)
			if err == nil && node == w.cfg.Node {
				_, _, _, at, _ := archiveIdentity(key)
				_, _, _, previous, _ := archiveIdentity(latest[id])
				if latest[id] == "" || at.After(previous) {
					latest[id] = key
				}
			}
		}
	}
	ids := make([]string, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	entries := make([]transferEntry, 0, len(ids))
	for _, id := range ids {
		key := latest[id]
		_, _, _, at, _ := archiveIdentity(key)
		entry := transferEntry{SchemaVersion: 1, SourceNode: w.cfg.Node, SandboxID: id, BackupAt: at.UTC().Format(time.RFC3339Nano), Bucket: w.cfg.Bucket, ArchiveKey: key, MetadataKey: metadataKey(key)}
		m, err := w.readRecovery(ctx, key)
		if err == nil {
			head, e := w.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(key)})
			if e != nil || aws.ToInt64(head.ContentLength) != m.ArchiveBytes || head.Metadata["sha256"] != m.ArchiveSHA256 {
				err = fmt.Errorf("archive/Metadata binding mismatch")
			}
		}
		if err != nil {
			entry.Problem = err.Error()
		} else {
			entry.Metadata = &m
			entry.Importable = true
		}
		entries = append(entries, entry)
	}
	return entries, nil
}
func (w *worker) adminExport(rw http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	entries, err := w.exportLatest(ctx)
	if err != nil {
		http.Error(rw, err.Error(), 502)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(entries)
}
func (w *worker) validateTransfer(e transferEntry) error {
	node, id, _, _, err := archiveIdentity(e.ArchiveKey)
	if err != nil {
		return err
	}
	allowed := node == w.cfg.Node
	for _, n := range strings.Split(w.cfg.ImportSources, ",") {
		if strings.TrimSpace(n) == node {
			allowed = true
		}
	}
	if !allowed || e.SourceNode != node || e.SandboxID != id || e.SchemaVersion != 1 || e.Bucket != w.cfg.Bucket || e.MetadataKey != metadataKey(e.ArchiveKey) || !e.Importable || e.Metadata == nil {
		return fmt.Errorf("unsupported source, bucket or incomplete backup")
	}
	return nil
}
