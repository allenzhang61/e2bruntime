package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type worker struct {
	cfg config
	api *api
	s3  *s3.Client
}

func main() {
	c, e := configInit()
	if e != nil {
		log.Fatal(e)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	w := worker{c, newAPI(c), s3.NewFromConfig(aws.Config{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AK, c.SK, "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.Endpoint); o.UsePathStyle = true })}
	if len(os.Args) == 3 && os.Args[1] == "clear-project-bucket" {
		if e = w.clearProjectBucket(ctx, os.Args[2]); e != nil {
			log.Fatal(e)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "admin" {
		if e = w.serveAdmin(ctx); e != nil {
			log.Fatal(e)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "status" {
		if e = w.status(ctx); e != nil {
			log.Fatal(e)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "seed" {
		if e = w.seed(ctx); e != nil {
			log.Fatal(e)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "hibernate" {
		if len(os.Args) != 3 {
			log.Fatal("usage: e2b-backup hibernate SANDBOX_ID")
		}
		operation, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		if e = w.hibernate(operation, os.Args[2]); e != nil {
			log.Fatal(e)
		}
		return
	}
	log.Printf("backup worker node=%s interval=%s keep=%d kind=filesystem", c.Node, c.Interval, c.Keep)
	// A single sequential loop prevents overlapping snapshots and retention races.
	defer os.Remove(w.schedulePath())
	lastCycle := time.Now()
	next := lastCycle.Add(w.cfg.Interval)
	if !w.cfg.BackupDisabled {
		w.cycleScheduled(ctx, next)
	}
	w.publishSchedule(backupSchedule{Status: "waiting", Next: next})
	// Re-read atomic settings without restarting the service or interrupting a backup.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		updated, err := loadBackupSettings(w.cfg)
		if err != nil {
			log.Print(err)
			return
		}
		wasDisabled := w.cfg.BackupDisabled
		w.cfg = updated
		if w.cfg.BackupDisabled {
			w.publishSchedule(backupSchedule{Status: "disabled"})
			continue
		}
		if wasDisabled {
			lastCycle = time.Now().Add(-w.cfg.Interval)
		}
		next = lastCycle.Add(w.cfg.Interval)
		if !time.Now().Before(next) {
			lastCycle = time.Now()
			next = lastCycle.Add(w.cfg.Interval)
			w.cycleScheduled(ctx, next)
		}
		w.publishSchedule(backupSchedule{Status: "waiting", Next: next})
	}
}
func (w *worker) cycle(ctx context.Context) { w.cycleScheduled(ctx, time.Time{}) }
func (w *worker) cycleScheduled(ctx context.Context, next time.Time) {
	state := backupSchedule{Status: "cycling", Next: next}
	if !next.IsZero() {
		w.publishSchedule(state)
	}
	sbs, e := w.api.running(ctx)
	if e != nil {
		log.Print(e)
		return
	}
	for _, sb := range sbs {
		cfg, err := loadBackupSettings(w.cfg)
		if err != nil || cfg.BackupDisabled {
			return
		}
		if ctx.Err() != nil {
			return
		}
		// Only intentionally persistent demo sandboxes have their TTL extended.
		if sb.Metadata["purpose"] == "periodic-backup-demo" {
			_, e = w.api.call(ctx, "POST", "/sandboxes/"+sb.ID+"/timeout", map[string]int{"timeout": 1800}, nil)
			if e != nil {
				log.Printf("renew sandbox=%s: %v", sb.ID, e)
			}
		}
		state.Current = sb.ID
		if !next.IsZero() {
			w.publishSchedule(state)
		}
		c, cancel := context.WithTimeout(ctx, 110*time.Second)
		e = w.backup(c, sb.ID)
		cancel()
		state.Current = ""
		state.Completed = append(state.Completed, sb.ID)
		if !next.IsZero() {
			w.publishSchedule(state)
		}
		if e != nil {
			log.Printf("backup failed sandbox=%s: %v", sb.ID, e)
		}
	}
}
func (w *worker) backup(ctx context.Context, id string) error {
	return w.withNodeLock(ctx, func() error { return w.backupOnce(ctx, id, false) })
}
func (w *worker) backupOnce(ctx context.Context, id string, verifyDownload bool) error {
	start := time.Now()
	before, e := w.api.configuration(ctx, id)
	if e != nil {
		return e
	}
	owner, ownerETag, e := w.backupPermission(ctx, before)
	if e != nil {
		return e
	}
	logicID, e := w.workspaceFor(ctx, before)
	if e != nil {
		return e
	}
	ctx = context.WithValue(ctx, workspaceContextKey{}, logicID)
	snap, build, e := w.api.snapshot(ctx, id)
	// Generated snapshot templates are temporary: the complete archive is independent.
	if snap != "" {
		defer func() {
			clean, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if e := w.api.deleteSnapshot(clean, snap); e != nil {
				log.Printf("snapshot cleanup failed id=%s: %v", snap, e)
			}
		}()
	}
	if e != nil {
		return e
	}
	after, e := w.api.configuration(ctx, id)
	if e != nil {
		return e
	}
	if !configurationStable(before, after) {
		return fmt.Errorf("sandbox configuration changed during snapshot; retry backup")
	}
	metadata, e := makeRecoveryMetadata(after, w.cfg.Node, snap, build)
	if e != nil {
		return e
	}
	snapshotMS := float64(time.Since(start).Microseconds()) / 1000
	f, e := os.CreateTemp("", "e2b-backup-*.tar.gz")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	ex := Exporter{StorageRoot: w.cfg.Storage, ExternalBuild: w.ensureLayer}
	stage := time.Now()
	manifest, e := ex.ExportBuild(ctx, Record{BuildID: build, Kind: "filesystem"}, io.MultiWriter(f, hash))
	if e != nil {
		return e
	}
	exportMS := float64(time.Since(stage).Microseconds()) / 1000
	size, e := f.Stat()
	if e != nil {
		return e
	}
	if _, e = f.Seek(0, 0); e != nil {
		return e
	}
	prefix := "backups/" + logicID + "/" + w.cfg.Node + "/" + id + "/filesystem/"
	key := prefix + time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + build + ".tar.gz"
	stage = time.Now()
	sha := hex.EncodeToString(hash.Sum(nil))
	pairCommitted := false
	defer func() {
		if pairCommitted {
			return
		}
		clean, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, object := range []string{key, metadataKey(key)} {
			if _, err := w.s3.DeleteObject(clean, &s3.DeleteObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(object)}); err != nil {
				log.Printf("incomplete backup cleanup failed key=%s", object)
			}
		}
	}()
	if e = w.checkGeneration(ctx, owner, ownerETag); e != nil {
		return e
	}
	metadataBytes, e := w.uploadMetadata(ctx, key, size.Size(), sha, metadata, manifest.External)
	if e != nil {
		return e
	}
	_, e = w.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(key), Body: f, Metadata: map[string]string{"sha256": sha, "snapshot-id": snap, "build-id": build, "kind": "filesystem", "metadata-key": metadataKey(key)}})
	if e != nil {
		return fmt.Errorf("S3 PUT failed (%T)", e)
	}
	head, e := w.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(key)})
	if e != nil || aws.ToInt64(head.ContentLength) != size.Size() || head.Metadata["sha256"] != sha {
		return fmt.Errorf("S3 HEAD verification failed")
	}
	if verifyDownload {
		object, err := w.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(key)})
		if err != nil {
			return fmt.Errorf("final backup download failed (%T)", err)
		}
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(object.Body, size.Size()+1))
		object.Body.Close()
		if err != nil || n != size.Size() || hex.EncodeToString(h.Sum(nil)) != sha {
			return fmt.Errorf("final backup download checksum/size mismatch")
		}
	}
	if e = w.checkGeneration(ctx, owner, ownerETag); e != nil {
		return e
	}
	pairCommitted = true
	uploadMS := float64(time.Since(stage).Microseconds()) / 1000
	deleted, count, e := w.retain(ctx, prefix)
	if e != nil {
		return e
	}
	log.Printf("backup complete sandbox=%s key=%s bytes=%d sha256=%s metadata_key=%s metadata_bytes=%d snapshot_ms=%.2f export_ms=%.2f upload_ms=%.2f total_ms=%.2f kept=%d deleted=%d", id, key, size.Size(), sha, metadataKey(key), metadataBytes, snapshotMS, exportMS, uploadMS, float64(time.Since(start).Microseconds())/1000, count, deleted)
	return nil
}
func (w *worker) seed(ctx context.Context) error {
	existing, e := w.api.running(ctx)
	if e != nil {
		return e
	}
	for _, sb := range existing {
		if sb.Metadata["purpose"] == "periodic-backup-demo" {
			fmt.Println("existing demo sandbox:", sb.ID)
			return nil
		}
	}
	var sb sandbox
	_, e = w.api.call(ctx, "POST", "/v2/sandboxes", map[string]any{"templateID": "base", "timeout": 1800, "metadata": map[string]string{"purpose": "periodic-backup-demo"}}, &sb)
	if e != nil {
		return e
	}
	_, e = w.api.RunCommand(ctx, sb, `(while true; do date -u +%FT%TZ >> /home/user/backup-demo.log; sleep 30; done) >/dev/null 2>&1 &`)
	if e != nil {
		return e
	}
	fmt.Println("created demo sandbox:", sb.ID, "file=/home/user/backup-demo.log (appends every 30s)")
	return nil
}
