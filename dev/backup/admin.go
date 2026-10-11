package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/smithy-go"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type adminSnapshot struct {
	MetadataKey   string `json:"metadataKey,omitempty"`
	MetadataBytes int64  `json:"metadataBytes,omitempty"`
	Key           string `json:"key"`
	SandboxID     string `json:"sandboxId"`
	Bytes         int64  `json:"bytes"`
	BackupAt      string `json:"backupAt"`
	StoredAt      string `json:"storedAt"`
	Kind          string `json:"kind"`
}

func (w *worker) adminHandler() http.Handler {
	manager := newImportManager(w)
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Cache-Control", "no-store")
		expected, got := sha256.Sum256([]byte(w.cfg.Key)), sha256.Sum256([]byte(r.Header.Get("X-API-Key")))
		if w.cfg.Key == "" || subtle.ConstantTimeCompare(expected[:], got[:]) != 1 {
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/schedule" {
			w.adminSchedule(rw, r)
			return
		}
		if r.URL.Path == "/ownership" || r.URL.Path == "/takeover" || r.URL.Path == "/prepare-import" {
			w.adminOwnership(rw, r)
			return
		}
		if r.URL.Path == "/imports" {
			manager.handle(rw, r)
			return
		}
		if r.URL.Path == "/export" {
			if r.Method != "GET" {
				http.Error(rw, "GET required", 405)
				return
			}
			w.adminExport(rw, r)
			return
		}
		if r.URL.Path != "/snapshots" && r.URL.Path != "/metadata" {
			http.NotFound(rw, r)
			return
		}
		if r.Method != http.MethodGet {
			rw.Header().Set("Allow", "GET")
			http.Error(rw, "method not allowed", 405)
			return
		}
		id := r.URL.Query().Get("sandboxId")
		if id != "" && !sandboxIDPattern.MatchString(id) {
			http.Error(rw, "invalid sandbox ID", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		prefix := "backups/"
		if r.URL.Path == "/metadata" {
			w.adminMetadata(ctx, rw, r, id)
			return
		}
		metadataObjects := map[string]int64{}
		pg := s3.NewListObjectsV2Paginator(w.s3, &s3.ListObjectsV2Input{Bucket: aws.String(w.cfg.Bucket), Prefix: aws.String(prefix)})
		snapshots := []adminSnapshot{}
		counts := map[string]int{}
		for pg.HasMorePages() {
			p, e := pg.NextPage(ctx)
			if e != nil {
				http.Error(rw, "backup storage unavailable", 502)
				return
			}
			for _, o := range p.Contents {
				key := aws.ToString(o.Key)
				if strings.HasPrefix(key, prefix) && strings.HasSuffix(key, ".metadata.json") {
					metadataObjects[key] = aws.ToInt64(o.Size)
				}
				parts := backupParts(key)
				if len(parts) != 5 || parts[0] != "backups" || parts[1] != w.cfg.Node || parts[3] != "filesystem" || !sandboxIDPattern.MatchString(parts[2]) || !strings.HasSuffix(parts[4], ".tar.gz") {
					continue
				}
				if id != "" && parts[2] != id {
					continue
				}
				stamp := strings.SplitN(parts[4], "-", 2)[0]
				at, e := time.Parse("20060102T150405.000000000Z", stamp)
				if e != nil {
					continue
				}
				snapshots = append(snapshots, adminSnapshot{Key: key, SandboxID: parts[2], Bytes: aws.ToInt64(o.Size), BackupAt: at.UTC().Format(time.RFC3339Nano), StoredAt: aws.ToTime(o.LastModified).UTC().Format(time.RFC3339Nano), Kind: "filesystem"})
				counts[parts[2]]++
			}
		}
		for i := range snapshots {
			key := metadataKey(snapshots[i].Key)
			if size, ok := metadataObjects[key]; ok {
				snapshots[i].MetadataKey = key
				snapshots[i].MetadataBytes = size
			}
		}
		sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].BackupAt > snapshots[j].BackupAt })
		rw.Header().Set("Content-Type", "application/json")
		if id == "" {
			json.NewEncoder(rw).Encode(map[string]any{"counts": counts, "node": w.cfg.Node})
			return
		}
		json.NewEncoder(rw).Encode(map[string]any{"snapshots": snapshots, "count": len(snapshots), "sandboxId": id, "node": w.cfg.Node})
	})
}
func (w *worker) serveAdmin(ctx context.Context) error {
	// S3 credentials and the team key remain local; the Dashboard is the public proxy.
	server := &http.Server{Addr: "127.0.0.1:3012", Handler: w.adminHandler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 6 * time.Minute, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(clean)
	}()
	log.Printf("backup admin node=%s listen=%s", w.cfg.Node, server.Addr)
	e := server.ListenAndServe()
	if e == http.ErrServerClosed {
		return nil
	}
	return fmt.Errorf("backup admin: %w", e)
}

func (w *worker) adminMetadata(ctx context.Context, rw http.ResponseWriter, r *http.Request, id string) {
	key := r.URL.Query().Get("archiveKey")
	node, instance, build, _, err := archiveIdentity(key)
	if err != nil || id == "" || node != w.cfg.Node || instance != id {
		http.Error(rw, "invalid archive key", 400)
		return
	}
	object, e := w.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(metadataKey(key))})
	if e != nil {
		var api smithy.APIError
		if errors.As(e, &api) && (api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "NotFound") {
			http.Error(rw, "no Metadata for this backup", 404)
			return
		}
		http.Error(rw, "metadata storage unavailable", 502)
		return
	}
	defer object.Body.Close()
	data, e := io.ReadAll(io.LimitReader(object.Body, (1<<20)+1))
	sum := sha256.Sum256(data)
	var m recoveryMetadata
	if e != nil || len(data) > 1<<20 || int64(len(data)) != aws.ToInt64(object.ContentLength) || hex.EncodeToString(sum[:]) != object.Metadata["sha256"] || json.Unmarshal(data, &m) != nil || m.ArchiveKey != key || m.SourceNode != w.cfg.Node || m.Sandbox.SandboxID != id || m.BuildID != build || m.SchemaVersion != 1 {
		http.Error(rw, "metadata verification failed", 502)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]any{"metadataKey": metadataKey(key), "metadata": json.RawMessage(data)})
}
