package main

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in, dry run by default. Preserve the sole running instance and
// the complete transitive External dependency map emitted by Exporter.
func TestPruneOtherSandboxBackupsLive(t *testing.T) {
	if os.Getenv("PRUNE_BACKUPS_LIVE") != "1" {
		t.Skip("PRUNE_BACKUPS_LIVE=1; deletion additionally requires PRUNE_BACKUPS_APPLY=1")
	}
	w := transferWorker(t)
	id := os.Getenv("PRUNE_KEEP_SANDBOX")
	if w.cfg.Node != "runtime1" || !sandboxIDPattern.MatchString(id) {
		t.Fatal("runtime1 and explicit PRUNE_KEEP_SANDBOX required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	err := w.withNodeLock(ctx, func() error {
		live, e := w.api.running(ctx)
		if e != nil {
			return e
		}
		var paused []sandbox
		next, e := w.api.call(ctx, "GET", "/v2/sandboxes?state=paused&limit=100", nil, &paused)
		if e != nil || next != "" {
			return fmt.Errorf("paused inventory incomplete")
		}
		live = append(live, paused...)
		ids := []string{}
		for _, sb := range live {
			ids = append(ids, sb.ID)
		}
		t.Logf("current instance IDs=%v", ids)
		if len(live) != 1 || live[0].ID != id {
			return fmt.Errorf("sole instance differs from keep ID: current=%v", ids)
		}
		list := func() (map[string]int64, error) {
			all := map[string]int64{}
			for _, prefix := range []string{"backups/", "control/backup-owners/"} {
				p := s3.NewListObjectsV2Paginator(w.s3, &s3.ListObjectsV2Input{Bucket: aws.String(w.cfg.Bucket), Prefix: aws.String(prefix)})
				for p.HasMorePages() {
					page, e := p.NextPage(ctx)
					if e != nil {
						return nil, fmt.Errorf("list failed (%T)", e)
					}
					for _, o := range page.Contents {
						all[aws.ToString(o.Key)] = aws.ToInt64(o.Size)
					}
				}
			}
			return all, nil
		}
		all, e := list()
		if e != nil {
			return e
		}
		keep := map[string]bool{ownershipKey("runtime1", id): true}
		archives := 0
		for key := range all {
			if !strings.HasPrefix(key, "backups/runtime1/"+id+"/filesystem/") {
				continue
			}
			keep[key] = true
			if !strings.HasSuffix(key, ".tar.gz") {
				continue
			}
			archives++
			m, e := w.readRecovery(ctx, key)
			if e != nil {
				return fmt.Errorf("keep archive %s: %w", key, e)
			}
			h, e := w.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(key)})
			if e != nil || aws.ToInt64(h.ContentLength) != m.ArchiveBytes || h.Metadata["sha256"] != m.ArchiveSHA256 {
				return fmt.Errorf("archive binding failed: %s", key)
			}
			for build, dep := range m.Dependencies {
				if !uuidPattern.MatchString(build) || dep != "backups/runtime1/layers/"+build+".tar.gz" {
					return fmt.Errorf("unexpected dependency %s", dep)
				}
				if _, ok := all[dep]; !ok {
					return fmt.Errorf("missing dependency %s", dep)
				}
				keep[dep] = true
			}
		}
		if archives == 0 {
			return fmt.Errorf("no archives to preserve")
		}
		if _, ok := all[ownershipKey("runtime1", id)]; !ok {
			return fmt.Errorf("current ownership record missing")
		}
		var remove []string
		var bytes int64
		for key, size := range all {
			if keep[key] {
				continue
			}
			valid := false
			if strings.HasPrefix(key, "control/backup-owners/") {
				p := strings.Split(key, "/")
				valid = len(p) == 4 && sandboxIDPattern.MatchString(p[2]) && strings.HasSuffix(p[3], ".json") && sandboxIDPattern.MatchString(strings.TrimSuffix(p[3], ".json"))
			} else if strings.HasSuffix(key, ".metadata.json") {
				_, _, _, _, e := archiveIdentity(strings.TrimSuffix(key, ".metadata.json") + ".tar.gz")
				valid = e == nil
			} else {
				_, _, _, _, e := archiveIdentity(key)
				valid = e == nil
				p := strings.Split(key, "/")
				if len(p) == 4 && p[0] == "backups" && sandboxIDPattern.MatchString(p[1]) && p[2] == "layers" && strings.HasSuffix(p[3], ".tar.gz") && uuidPattern.MatchString(strings.TrimSuffix(p[3], ".tar.gz")) {
					valid = true
				}
			}
			if !valid {
				return fmt.Errorf("unrecognized managed object; refusing cleanup: %s", key)
			}
			remove = append(remove, key)
			bytes += size
		}
		sort.Strings(remove)
		t.Logf("keep=%s archives=%d preserved_objects=%d delete_objects=%d delete_bytes=%d", id, archives, len(keep), len(remove), bytes)
		for _, key := range remove {
			t.Logf("DELETE %s (%d bytes)", key, all[key])
		}
		if os.Getenv("PRUNE_BACKUPS_APPLY") != "1" {
			t.Log("dry run; nothing deleted")
			return nil
		}
		for _, key := range remove {
			if _, e := w.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(key)}); e != nil {
				return fmt.Errorf("delete failed %s (%T)", key, e)
			}
		}
		final, e := list()
		if e != nil {
			return e
		}
		for key := range final {
			if !keep[key] {
				return fmt.Errorf("unexpected remaining %s", key)
			}
		}
		for key := range keep {
			if _, ok := final[key]; !ok {
				return fmt.Errorf("preserved object missing %s", key)
			}
		}
		t.Logf("verified remaining_objects=%d deleted_objects=%d deleted_bytes=%d", len(final), len(remove), bytes)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
