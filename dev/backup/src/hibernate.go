package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

var sandboxIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// The worker and CLI share a node-wide lock, including shared-layer uploads
// and retention. The file is never unlinked: flock is released on process exit.
func (w *worker) withNodeLock(ctx context.Context, fn func() error) error {
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "e2b-backup-"+w.cfg.Node+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

// backupBeforeHibernate must never call pause after a failed backup.
func backupBeforeHibernate(ctx context.Context, backup, pause func(context.Context) error) error {
	if err := backup(ctx); err != nil {
		return fmt.Errorf("final backup failed; hibernate not requested: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := pause(ctx); err != nil {
		return fmt.Errorf("backup saved, but hibernate request failed (check sandbox state): %w", err)
	}
	return nil
}

func (w *worker) hibernate(ctx context.Context, id string) error {
	if !sandboxIDPattern.MatchString(id) {
		return fmt.Errorf("invalid sandbox ID")
	}
	return w.withNodeLock(ctx, func() error {
		running, err := w.api.running(ctx)
		if err != nil {
			return err
		}
		var sb *sandbox
		for i := range running {
			if running[i].ID == id {
				sb = &running[i]
				break
			}
		}
		if sb == nil {
			return fmt.Errorf("sandbox is not running on this Runtime/team")
		}
		// Listing omits the guest access token. Connect obtains credentials and
		// reserves enough TTL for the final backup; on failure this is a renewal.
		var connected sandbox
		if _, err := w.api.call(ctx, "POST", "/v2/sandboxes/"+url.PathEscape(id)+"/connect", map[string]int{"timeout": 600}, &connected); err != nil {
			return err
		}
		sb = &connected
		// sync flushes kernel buffers; the caller must separately persist application
		// state and quiesce writers throughout this operation.
		if _, err := w.api.RunCommand(ctx, *sb, "sync"); err != nil {
			return fmt.Errorf("sync before backup failed: %w", err)
		}
		start := time.Now()
		err = backupBeforeHibernate(ctx,
			func(ctx context.Context) error { return w.backupOnce(ctx, id, true) },
			func(ctx context.Context) error {
				pauseStart := time.Now()
				_, err := w.api.call(ctx, "POST", "/sandboxes/"+url.PathEscape(id)+"/pause", map[string]bool{"memory": true}, nil)
				if err == nil {
					log.Printf("hibernate complete sandbox=%s pause_ms=%.2f total_ms=%.2f", id, float64(time.Since(pauseStart).Microseconds())/1000, float64(time.Since(start).Microseconds())/1000)
				}
				return err
			})
		return err
	})
}
