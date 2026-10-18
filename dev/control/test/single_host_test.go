package e2e

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// Single-node workflow shares the existing Control client and inventory types.
func TestSingleHostEndToEnd(t *testing.T) {
	if os.Getenv("CONTROL_SINGLE_HOST_E2E") != "1" {
		t.Skip("set CONTROL_SINGLE_HOST_E2E=1 and CONTROL_E2E=1")
	}
	cfg := configInit(t)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	s := &suite{cfg: cfg, ctx: ctx, http: &http.Client{Timeout: time.Minute}, created: map[string]string{}}
	var inventory struct {
		Runtimes []struct {
			Name string `json:"name"`
		} `json:"runtimes"`
	}
	s.must(t, "GET", "/api/logics", nil, &inventory)
	if len(inventory.Runtimes) != 1 {
		t.Fatal("single-node Control required")
	}
	node := inventory.Runtimes[0].Name
	created := s.debug(t, node, "create", map[string]any{"template_id": "base", "timeout": 600})
	id, _ := created["sandboxID"].(string)
	if id == "" {
		t.Fatal("sandbox ID missing")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), cfg.Poll*20)
		defer stop()
		s.ctx = cleanup
		s.debug(t, node, "delete", map[string]any{"sandbox_id": id})
	})
	s.created[node] = id
	s.debug(t, node, "write-file", map[string]any{"sandbox_id": id, "path": "/home/user/deployment-check.txt", "content": "single-host verification"})
	read := s.debug(t, node, "read-file", map[string]any{"sandbox_id": id, "path": "/home/user/deployment-check.txt"})
	if read["content"] != "single-host verification" {
		t.Fatal("file read-back mismatch")
	}
	if s.command(t, node, id, "printf single-host-command-ok") != "single-host-command-ok" {
		t.Fatal("command output mismatch")
	}
	s.wait(t, cfg.BackupWait, "complete filesystem backup", func() (bool, error) {
		for _, row := range s.inventory(t) {
			for _, img := range row.Images {
				if img.Node == node && img.ID == id && img.Key != "" && img.Metadata != "" {
					s.logic = row.ID
					return true, nil
				}
			}
		}
		return false, nil
	})
	t.Logf("verified %s/%s; test sandbox will be deleted, S3 backup retained", node, id)
}
