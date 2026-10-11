package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Recover only a proven DB-registration failure that occurred before create API.
func TestControllerPrecreateRepairLive(t *testing.T) {
	if os.Getenv("CONTROL_PRECREATE_REPAIR") != "1" {
		t.Skip("explicit opt-in required")
	}
	w := transferWorker(t)
	if w.cfg.Node != "runtime2" {
		t.Fatal("runtime2 only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := w.withNodeLock(ctx, func() error {
		n, id := "runtime1", "i3boc9qul6uff777pdd2q"
		if e := w.requireLogicalVacancy(ctx, n, id); e != nil {
			return e
		}
		owner, etag, e := w.readOwner(ctx, n, id)
		if e != nil {
			return e
		}
		if owner.OwnerNode != "runtime2" || owner.State != "importing" || owner.OwnerSandboxID != "" {
			return fmt.Errorf("not an incomplete precreate owner")
		}
		entries, e := os.ReadDir(w.cfg.ImportJobs)
		if e != nil {
			return e
		}
		proven := false
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			raw, e := os.ReadFile(filepath.Join(w.cfg.ImportJobs, entry.Name()))
			if e != nil {
				continue
			}
			var job importJob
			if json.Unmarshal(raw, &job) != nil {
				continue
			}
			for _, r := range job.Results {
				if r.ArchiveKey == owner.PinnedArchive && r.Error == "target template registration conflict or failure" && r.Status == "failed" && r.SandboxID == "" {
					proven = true
				}
			}
		}
		if !proven {
			return fmt.Errorf("no proven precreate job failure")
		}
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/generation/%d", w.cfg.Bucket, owner.PinnedArchive, owner.Generation-1)))
		raw, e := os.ReadFile(filepath.Join(w.cfg.ImportJobs, "archives", hex.EncodeToString(sum[:])+".json"))
		if e != nil {
			return e
		}
		var record importResult
		if json.Unmarshal(raw, &record) != nil || record.Status != "creating" || record.SandboxID != "" {
			return fmt.Errorf("unsafe ledger phase")
		}
		owner.State = "failed_before_create"
		owner.Generation++
		_, e = w.writeOwner(ctx, owner, etag)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestControllerRestoredFilesLive(t *testing.T) {
	if os.Getenv("CONTROL_RESTORE_VERIFY") != "1" {
		t.Skip("explicit opt-in")
	}
	w := transferWorker(t)
	if w.cfg.Node != "runtime2" {
		t.Fatal("runtime2 only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	own, _, e := w.readOwner(ctx, "runtime1", "i3boc9qul6uff777pdd2q")
	if e != nil || own.OwnerNode != "runtime2" || own.State != "active" || own.OwnerSandboxID == "" {
		t.Fatal("restored ownership missing")
	}
	var sb sandbox
	_, e = w.api.call(ctx, "POST", "/sandboxes/"+own.OwnerSandboxID+"/connect", map[string]int{"timeout": 1800}, &sb)
	if e != nil {
		t.Fatal(e)
	}
	configuration, e := w.api.configuration(ctx, sb.ID)
	if e != nil {
		t.Fatal(e)
	}
	n, id, e := logicalIdentity("runtime2", sb.ID, configuration.Metadata)
	if e != nil || n != "runtime1" || id != "i3boc9qul6uff777pdd2q" {
		t.Fatal("logical identity changed")
	}
	result, e := w.api.RunCommand(ctx, sb, "cat /home/user/backup-demo.log")
	if e != nil {
		t.Fatal(e)
	}
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) < 2 {
		t.Fatal("restored file lacks history")
	}
	last, e := time.Parse(time.RFC3339, lines[len(lines)-1])
	if e != nil {
		t.Fatal(e)
	}
	m, e := w.readRecovery(ctx, own.PinnedArchive)
	if e != nil {
		t.Fatal(e)
	}
	at, e := time.Parse(time.RFC3339Nano, m.CapturedAt)
	if e != nil || last.After(at) {
		t.Fatalf("restored file timestamp mismatch: last=%s captured=%s difference=%s parse_error=%v lines=%d", last.Format(time.RFC3339), m.CapturedAt, at.Sub(last), e, len(lines))
	}
	for _, line := range lines {
		stamp, err := time.Parse(time.RFC3339, line)
		if err != nil || stamp.After(at) {
			t.Fatal("file includes invalid or post-backup history")
		}
	}
	if w.requireLogicalVacancy(ctx, n, id) == nil {
		t.Fatal("existing Logic ID was not rejected")
	}
	t.Logf("verified Logic ID=%s/%s instance=%s file_lines=%d file_last_utc=%s backup_utc=%s", n, id, sb.ID, len(lines), last.Format(time.RFC3339), m.CapturedAt)
}
