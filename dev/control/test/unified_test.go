package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

type plan struct {
	Sleep  *time.Time `json:"sleep_at"`
	Delete *time.Time `json:"delete_at"`
	Error  string     `json:"error"`
}
type instance struct {
	Node   string    `json:"runtime"`
	ID     string    `json:"instance_id"`
	State  string    `json:"state"`
	Backup string    `json:"backup_state"`
	Next   time.Time `json:"next_backup_at"`
	Plan   *plan     `json:"plan"`
}
type image struct {
	Node          string    `json:"source_node"`
	ID            string    `json:"source_sandbox_id"`
	Key           string    `json:"archive_key"`
	Metadata      string    `json:"metadata_key"`
	Bytes         int64     `json:"archive_bytes"`
	MetadataBytes int64     `json:"metadata_bytes"`
	At            time.Time `json:"backup_at_utc"`
}
type logic struct {
	ID      string     `json:"logic_id"`
	Current []instance `json:"current_instances"`
	All     []instance `json:"instances"`
	Images  []image    `json:"images"`
	Owner   *struct {
		Node string `json:"owner_node"`
		ID   string `json:"owner_sandbox_id"`
	} `json:"owner"`
}

func (s *suite) inventory(t *testing.T) []logic {
	t.Helper()
	var data struct {
		Logics []logic           `json:"logics"`
		Errors map[string]string `json:"runtime_errors"`
	}
	s.must(t, "GET", "/api/logics", nil, &data)
	for node, err := range data.Errors {
		if err != "" {
			t.Fatalf("%s inventory unavailable: %s", node, err)
		}
	}
	return data.Logics
}
func (s *suite) row(t *testing.T) logic {
	t.Helper()
	for _, r := range s.inventory(t) {
		if r.ID == s.logic {
			return r
		}
	}
	t.Fatalf("Logic ID disappeared: %s", s.logic)
	return logic{}
}
func (s *suite) state(t *testing.T, node, id, state string) {
	t.Helper()
	s.wait(t, time.Minute, node+" state "+state, func() (bool, error) {
		for _, r := range s.inventory(t) {
			for _, i := range r.All {
				if i.Node == node && i.ID == id {
					return i.State == state, nil
				}
			}
		}
		return state == "absent", nil
	})
}
func (s *suite) schedule(t *testing.T, node, id, action string, at *time.Time) {
	t.Helper()
	body := map[string]any{"runtime": node, "sandbox_id": id, "action": action}
	if at != nil {
		body["at"] = at.UTC()
	}
	s.must(t, "POST", "/api/instances/schedule", body, nil)
}
func (s *suite) command(t *testing.T, node, id, command string) string {
	t.Helper()
	r := s.debug(t, node, "command", map[string]any{"sandbox_id": id, "command": command})
	if r["ExitCode"] != float64(0) {
		t.Fatalf("guest command failed: %+v", r)
	}
	out, _ := r["Stdout"].(string)
	return strings.TrimSpace(out)
}
func (s *suite) backup(t *testing.T, node, id string, after time.Time) {
	t.Helper()
	s.wait(t, s.cfg.BackupWait, "complete S3 backup for "+node+"/"+id, func() (bool, error) {
		for _, i := range s.row(t).Images {
			if i.Node == node && i.ID == id && i.At.After(after) {
				if i.Bytes <= 0 || i.MetadataBytes <= 0 || i.Metadata == "" || !strings.HasPrefix(i.Key, "backups/"+s.logic+"/") {
					return false, fmt.Errorf("invalid archive/Metadata information: %+v", i)
				}
				return true, nil
			}
		}
		return false, nil
	})
}
func (s *suite) restore(t *testing.T) string {
	t.Helper()
	var submitted struct {
		ID string `json:"id"`
	}
	s.must(t, "POST", "/api/restore", map[string]any{"logic_id": s.logic}, &submitted)
	if submitted.ID == "" {
		t.Fatal("missing recovery job ID")
	}
	id := ""
	s.wait(t, 4*time.Minute, "recovery job "+submitted.ID, func() (bool, error) {
		var jobs []struct {
			ID, Status string
			Instance   string `json:"instance_id"`
			Error      string `json:"error"`
		}
		s.must(t, "GET", "/api/jobs", nil, &jobs)
		for _, j := range jobs {
			if j.ID == submitted.ID {
				if j.Instance != "" {
					s.created[j.Instance] = "runtime2"
				}
				if j.Status == "failed" {
					return false, fmt.Errorf("restore: %s", j.Error)
				}
				if j.Status == "succeeded" {
					id = j.Instance
					return id != "", nil
				}
				return false, nil
			}
		}
		return false, fmt.Errorf("job missing")
	})
	r := s.row(t)
	if len(r.Current) != 1 || r.Current[0].ID != id || r.Current[0].Node != "runtime2" || r.Owner == nil || r.Owner.ID != id {
		t.Fatal("restored instance not bound in unified view")
	}
	return id
}
func TestUnifiedViewEndToEnd(t *testing.T) {
	cfg := configInit(t)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()
	s := suite{cfg: cfg, ctx: ctx, http: &http.Client{Timeout: 55 * time.Second}, created: map[string]string{}}
	t.Cleanup(func() {
		defer s.report(t)
		clean, stop := context.WithTimeout(context.Background(), 2*time.Minute)
		defer stop()
		s.ctx = clean
		for id, node := range s.created {
			found := false
			for _, r := range s.inventory(t) {
				for _, i := range r.All {
					if i.Node == node && i.ID == id {
						found = true
					}
				}
			}
			if found {
				if _, err := s.call("POST", "/api/debug/delete", map[string]any{"runtime": node, "sandbox_id": id}, nil); err != nil {
					t.Errorf("cleanup %s/%s: %v", node, id, err)
				}
			}
		}
	})
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	marker := "control-e2e-" + hex.EncodeToString(nonce[:])
	file := "/home/user/" + marker + ".txt"
	source := ""
	boot := ""
	var written time.Time
	s.step(t, "create and unified inventory", func() {
		r := s.debug(t, "runtime1", "create", map[string]any{"timeout": 1800})
		source, _ = r["sandboxID"].(string)
		if source == "" {
			t.Fatal("missing created ID")
		}
		s.created[source] = "runtime1"
		s.wait(t, time.Minute, "workspace identity", func() (bool, error) {
			for _, row := range s.inventory(t) {
				for _, i := range row.All {
					if i.Node == "runtime1" && i.ID == source {
						s.logic = row.ID
						return true, nil
					}
				}
			}
			return false, nil
		})
		if !regexp.MustCompile(`^workspace-[a-z0-9]{6}$`).MatchString(s.logic) {
			t.Fatal("unstable or node-bound Logic ID", s.logic)
		}
	})
	s.step(t, "guest file and process marker", func() {
		s.debug(t, "runtime1", "write-file", map[string]any{"sandbox_id": source, "path": file, "content": marker})
		if s.debug(t, "runtime1", "read-file", map[string]any{"sandbox_id": source, "path": file})["content"] != marker {
			t.Fatal("file write/read mismatch")
		}
		boot = s.command(t, "runtime1", source, "cat /proc/sys/kernel/random/boot_id")
		s.command(t, "runtime1", source, "(exec -a "+marker+" sleep 1800) >/dev/null 2>&1 &")
		written = time.Now()
	})
	s.step(t, "independent sleep/delete plans and cancellation", func() {
		sleep := time.Now().Add(5 * time.Minute)
		del := time.Now().Add(10 * time.Minute)
		s.schedule(t, "runtime1", source, "set_sleep", &sleep)
		s.schedule(t, "runtime1", source, "set_delete", &del)
		s.schedule(t, "runtime1", source, "cancel_sleep", nil)
		p := s.row(t).Current[0].Plan
		if p == nil || p.Sleep != nil || p.Delete == nil || !p.Delete.Equal(del) {
			t.Fatal("cancel sleep affected delete plan")
		}
		s.schedule(t, "runtime1", source, "set_sleep", &sleep)
		s.schedule(t, "runtime1", source, "cancel_delete", nil)
		p = s.row(t).Current[0].Plan
		if p.Sleep == nil || !p.Sleep.Equal(sleep) || p.Delete != nil {
			t.Fatal("cancel delete affected sleep plan")
		}
		s.schedule(t, "runtime1", source, "cancel_sleep", nil)
	})
	s.step(t, "scheduled sleep and manual wake retain memory", func() {
		at := time.Now().Add(20 * time.Second)
		s.schedule(t, "runtime1", source, "set_sleep", &at)
		s.state(t, "runtime1", source, "paused")
		s.debug(t, "runtime1", "wake", map[string]any{"sandbox_id": source, "timeout": 1800})
		s.state(t, "runtime1", source, "running")
		if s.command(t, "runtime1", source, "cat /proc/sys/kernel/random/boot_id") != boot || s.command(t, "runtime1", source, "pgrep -f '^"+marker+" ' >/dev/null && echo alive") != "alive" {
			t.Fatal("memory pause lost process/boot state")
		}
	})
	s.step(t, "periodic backup includes archive and sidecar", func() {
		s.backup(t, "runtime1", source, written)
		r := s.row(t)
		if r.Owner == nil || r.Owner.Node != "runtime1" || r.Owner.ID != source {
			t.Fatal("backup ownership not registered")
		}
		for _, path := range []string{"/logics/" + url.PathEscape(s.logic) + "/backups", "/logics/" + url.PathEscape(s.logic) + "/instances"} {
			s.must(t, "GET", path, nil, nil)
		}
	})
	s.step(t, "switch clears current view and preserves source observations", func() {
		s.must(t, "POST", "/api/switch", map[string]any{"logic_id": s.logic, "target_runtime": "runtime2"}, nil)
		r := s.row(t)
		if r.Owner == nil || r.Owner.Node != "runtime2" || len(r.Current) != 0 {
			t.Fatal("switch still displays old current instance")
		}
		if len(r.All) != 1 || r.All[0].ID != source {
			t.Fatal("switch altered source instance")
		}
	})
	target := ""
	s.step(t, "restore to Runtime 2 preserves file but cold boots", func() {
		target = s.restore(t)
		if target == source {
			t.Fatal("physical instance ID reused")
		}
		if s.debug(t, "runtime2", "read-file", map[string]any{"sandbox_id": target, "path": file})["content"] != marker {
			t.Fatal("cross-node recovery lost file")
		}
		if s.command(t, "runtime2", target, "cat /proc/sys/kernel/random/boot_id") == boot {
			t.Fatal("filesystem restore reused old boot state")
		}
		if s.command(t, "runtime2", target, "if pgrep -f '^"+marker+" ' >/dev/null; then echo old; else echo absent; fi") != "absent" {
			t.Fatal("filesystem restore retained old process")
		}
		status, _ := s.call("POST", "/api/restore", map[string]any{"logic_id": s.logic}, nil)
		if status != 409 {
			t.Fatal("duplicate logical restore was accepted", status)
		}
	})
	s.step(t, "manual sleep and wake", func() {
		s.debug(t, "runtime2", "hibernate", map[string]any{"sandbox_id": target})
		s.state(t, "runtime2", target, "paused")
		s.debug(t, "runtime2", "wake", map[string]any{"sandbox_id": target, "timeout": 1800})
		s.state(t, "runtime2", target, "running")
	})
	s.step(t, "backup restored instance then delete it", func() {
		s.backup(t, "runtime2", target, written)
		s.debug(t, "runtime2", "delete", map[string]any{"sandbox_id": target})
		s.state(t, "runtime2", target, "absent")
		if len(s.row(t).Current) != 0 {
			t.Fatal("deleted target still shown as current")
		}
	})
	s.step(t, "restore after deletion from same-node backup regression", func() {
		latest := s.row(t).Images[0]
		if latest.Node != "runtime2" || latest.ID != target {
			t.Fatal("expected latest target-node snapshot")
		}
		target = s.restore(t)
		if s.debug(t, "runtime2", "read-file", map[string]any{"sandbox_id": target, "path": file})["content"] != marker {
			t.Fatal("delete/recovery lost marker")
		}
	})
	s.step(t, "scheduled delete removes only target instance", func() {
		at := time.Now().Add(20 * time.Second)
		s.schedule(t, "runtime2", target, "set_delete", &at)
		s.state(t, "runtime2", target, "absent")
		if len(s.row(t).Current) != 0 {
			t.Fatal("auto-deleted instance still current")
		}
	})
	s.step(t, "switch back binds existing original instance", func() {
		s.must(t, "POST", "/api/switch", map[string]any{"logic_id": s.logic, "target_runtime": "runtime1"}, nil)
		r := s.row(t)
		if len(r.Current) != 1 || r.Current[0].ID != source || r.Owner == nil || r.Owner.ID != source {
			t.Fatal("switch back did not rebind original instance")
		}
	})
}
