package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
)

type metadata struct {
	Schema       int               `json:"schema_version"`
	Node         string            `json:"source_node"`
	Archive      string            `json:"archive_key"`
	Bytes        int64             `json:"archive_bytes"`
	SHA          string            `json:"archive_sha256"`
	At           string            `json:"captured_at_utc"`
	Dependencies map[string]string `json:"external_build_archives"`
	Sandbox      struct {
		ID       string            `json:"sandboxID"`
		Metadata map[string]string `json:"metadata"`
	} `json:"sandbox"`
}
type imageFile struct {
	Key   string `json:"key"`
	Bytes int64  `json:"bytes"`
}
type image struct {
	Node          string          `json:"source_node"`
	Instance      string          `json:"source_sandbox_id"`
	Archive       string          `json:"archive_key"`
	MetadataKey   string          `json:"metadata_key"`
	MetadataBytes int64           `json:"metadata_bytes"`
	Bytes         int64           `json:"archive_bytes"`
	At            string          `json:"backup_at_utc"`
	Files         []imageFile     `json:"dependencies"`
	Raw           json.RawMessage `json:"-"`
}
type owner struct {
	Archive    string `json:"pinned_archive"`
	Node       string `json:"owner_node"`
	Instance   string `json:"owner_sandbox_id"`
	State      string `json:"state"`
	Generation int64  `json:"generation"`
}
type instance struct {
	Runtime     string        `json:"runtime"`
	ID          string        `json:"instance_id"`
	State       string        `json:"state"`
	EndAt       string        `json:"end_at,omitempty"`
	OnTimeout   string        `json:"on_timeout,omitempty"`
	BackupState string        `json:"backup_state"`
	NextBackup  string        `json:"next_backup_at,omitempty"`
	Plan        *instancePlan `json:"plan,omitempty"`
}
type logic struct {
	CurrentState     string     `json:"current_instance_state"`
	CurrentInstances []instance `json:"current_instances"`
	ID               string     `json:"logic_id"`
	OriginNode       string     `json:"origin_runtime"`
	OriginID         string     `json:"origin_instance_id"`
	Owner            *owner     `json:"owner"`
	Instances        []instance `json:"instances"`
	Images           []image    `json:"images"`
	Conflicts        []string   `json:"conflicts,omitempty"`
}
type inventory struct {
	Logics   []logic           `json:"logics"`
	Runtimes []runtimeConfig   `json:"runtimes"`
	Errors   map[string]string `json:"runtime_errors"`
	At       string            `json:"checked_at_utc"`
}

func identity(node, id string, attrs map[string]string) (string, string, error) {
	n, s := attrs["dev_backup_origin_node"], attrs["dev_backup_origin_id"]
	if n == "" && s == "" {
		n, s = node, id
	}
	for _, v := range []string{n, s} {
		if v == "" || strings.ContainsAny(v, "/\\. \n\r") {
			return "", "", fmt.Errorf("invalid logical identity")
		}
	}
	return n, s, nil
}
func (s *server) object(ctx context.Context, key string, limit int64) ([]byte, map[string]string, error) {
	o, e := s.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key)})
	if e != nil {
		return nil, nil, fmt.Errorf("S3 object unavailable: %s", key)
	}
	defer o.Body.Close()
	b, e := io.ReadAll(io.LimitReader(o.Body, limit+1))
	if e != nil || int64(len(b)) > limit || int64(len(b)) != aws.ToInt64(o.ContentLength) {
		return nil, nil, fmt.Errorf("invalid object size: %s", key)
	}
	return b, o.Metadata, nil
}
func (s *server) inventory(ctx context.Context) (inventory, error) {
	result := inventory{Logics: []logic{}, Runtimes: s.cfg.Runtimes, Errors: map[string]string{}, At: time.Now().UTC().Format(time.RFC3339)}
	rows := map[string]*logic{}
	get := func(n, id string) *logic {
		key := n + "/" + id
		if rows[key] == nil {
			rows[key] = &logic{ID: key, OriginNode: n, OriginID: id, Images: []image{}, Instances: []instance{}}
		}
		return rows[key]
	}
	sizes := map[string]int64{}
	p := s3.NewListObjectsV2Paginator(s.s3, &s3.ListObjectsV2Input{Bucket: aws.String(s.cfg.Bucket), Prefix: aws.String("backups/")})
	for p.HasMorePages() {
		page, e := p.NextPage(ctx)
		if e != nil {
			return result, fmt.Errorf("S3 inventory unavailable")
		}
		for _, o := range page.Contents {
			sizes[aws.ToString(o.Key)] = aws.ToInt64(o.Size)
		}
	}
	for key, size := range sizes {
		parts := strings.Split(key, "/")
		if len(parts) == 6 && workspacePattern.MatchString(parts[1]) {
			parts = append([]string{parts[0]}, parts[2:]...)
		}
		if len(parts) != 5 || parts[3] != "filesystem" || !strings.HasSuffix(key, ".metadata.json") {
			continue
		}
		raw, headers, e := s.object(ctx, key, 1<<20)
		if e != nil {
			return result, e
		}
		sum := sha256.Sum256(raw)
		var m metadata
		if json.Unmarshal(raw, &m) != nil || m.Schema != 1 || m.Node != parts[1] || m.Sandbox.ID != parts[2] || m.Archive != strings.TrimSuffix(key, ".metadata.json")+".tar.gz" || hex.EncodeToString(sum[:]) != headers["sha256"] {
			return result, fmt.Errorf("invalid backup Metadata: %s", key)
		}
		// A sidecar is uploaded before its archive; skip incomplete publication.
		archiveSize, ok := sizes[m.Archive]
		if !ok {
			continue
		}
		if archiveSize != m.Bytes {
			return result, fmt.Errorf("archive size mismatch")
		}
		n, id, e := identity(m.Node, m.Sandbox.ID, m.Sandbox.Metadata)
		if e != nil {
			return result, e
		}
		img := image{Node: m.Node, Instance: m.Sandbox.ID, Archive: m.Archive, MetadataKey: key, MetadataBytes: size, Bytes: m.Bytes, At: m.At, Raw: raw, Files: []imageFile{}}
		for _, dep := range m.Dependencies {
			bytes, ok := sizes[dep]
			if !ok {
				return result, fmt.Errorf("backup dependency missing: %s", dep)
			}
			img.Files = append(img.Files, imageFile{dep, bytes})
		}
		sort.Slice(img.Files, func(i, j int) bool { return img.Files[i].Key < img.Files[j].Key })
		row := get(n, id)
		row.Images = append(row.Images, img)
	}
	schedules := map[string]struct {
		Status    string   `json:"status"`
		Next      string   `json:"next_cycle_at"`
		Current   string   `json:"current_instance_id"`
		Processed []string `json:"processed_instances"`
	}{}
	for _, rt := range s.cfg.Runtimes {
		var schedule struct {
			Status    string   `json:"status"`
			Next      string   `json:"next_cycle_at"`
			Current   string   `json:"current_instance_id"`
			Processed []string `json:"processed_instances"`
		}
		if _, err := s.call(ctx, rt, true, "GET", "/schedule", nil, &schedule); err == nil {
			schedules[rt.Name] = schedule
		}

		var all []struct {
			ID       string            `json:"sandboxID"`
			State    string            `json:"state"`
			Metadata map[string]string `json:"metadata"`
		}
		token := ""
		seen := map[string]bool{}
		for {
			path := "/v2/sandboxes?state=running,paused&limit=100"
			if token != "" {
				path += "&nextToken=" + url.QueryEscape(token)
			}
			var page []struct {
				ID       string            `json:"sandboxID"`
				State    string            `json:"state"`
				Metadata map[string]string `json:"metadata"`
			}
			next, e := s.call(ctx, rt, false, "GET", path, nil, &page)
			if e != nil {
				result.Errors[rt.Name] = "Runtime unavailable; S3 backups remain visible"
				break
			}
			all = append(all, page...)
			if next == "" {
				break
			}
			if seen[next] {
				result.Errors[rt.Name] = "Invalid pagination"
				break
			}
			seen[next] = true
			token = next
		}
		for _, sb := range all {
			n, id, e := identity(rt.Name, sb.ID, sb.Metadata)
			if e != nil {
				result.Errors[rt.Name] = "Invalid logical metadata"
				continue
			}
			row := get(n, id)
			observed := instance{Runtime: rt.Name, ID: sb.ID, State: sb.State}
			if sb.State == "running" {
				// The list API omits lifecycle; read the instance's actual timeout policy.
				var info struct {
					ID        string `json:"sandboxID"`
					State     string `json:"state"`
					EndAt     string `json:"endAt"`
					Lifecycle struct {
						OnTimeout string `json:"onTimeout"`
					} `json:"lifecycle"`
				}
				if _, err := s.call(ctx, rt, false, "GET", "/sandboxes/"+url.PathEscape(sb.ID), nil, &info); err == nil && info.ID == sb.ID {
					observed.State = info.State
					if info.State == "running" {
						observed.EndAt = info.EndAt
						observed.OnTimeout = info.Lifecycle.OnTimeout
					}
				}
			}
			row.Instances = append(row.Instances, observed)
		}
	}
	planMu.Lock()
	plans, planErr := s.readPlans()
	planMu.Unlock()
	if planErr != nil {
		return result, planErr
	}
	for _, row := range rows {
		for idx := range row.Instances {
			i := &row.Instances[idx]
			if plan, ok := plans[i.Runtime+"/"+i.ID]; ok {
				i.Plan = &plan
			}
		}
	}
	names, err := s.workspaceIDs(ctx, rows)
	if err != nil {
		return result, err
	}
	for _, row := range rows {
		sort.Slice(row.Images, func(i, j int) bool {
			a, _ := time.Parse(time.RFC3339Nano, row.Images[i].At)
			b, _ := time.Parse(time.RFC3339Nano, row.Images[j].At)
			return a.After(b)
		})
		raw, _, e := s.object(ctx, "control/backup-owners/"+row.ID+".json", 16384)
		if e == nil {
			var o owner
			if json.Unmarshal(raw, &o) != nil {
				return result, fmt.Errorf("invalid owner record")
			}
			row.Owner = &o
		}
		row.CurrentState = "unassigned"
		if row.Owner != nil && row.Owner.Instance != "" {
			row.CurrentState = "missing"
			if result.Errors[row.Owner.Node] != "" {
				row.CurrentState = "unknown"
			}
			for _, i := range row.Instances {
				if i.Runtime == row.Owner.Node && i.ID == row.Owner.Instance {
					row.CurrentState = i.State
				}
			}
		}
		for idx := range row.Instances {
			i := &row.Instances[idx]
			i.BackupState = "unknown"
			if i.State == "paused" {
				i.BackupState = "paused"
				continue
			}
			if i.State != "running" {
				continue
			}
			canBackup := row.Owner != nil && row.Owner.Node == i.Runtime && row.Owner.Instance == i.ID && row.Owner.State == "active"
			if row.Owner == nil && row.OriginNode == i.Runtime && row.OriginID == i.ID {
				canBackup = true
			}
			if !canBackup {
				i.BackupState = "not_owner"
				continue
			}
			schedule := schedules[i.Runtime]
			if schedule.Status == "disabled" {
				i.BackupState = "disabled"
				continue
			}
			if schedule.Status == "stopped" {
				i.BackupState = "stopped"
				continue
			}
			if schedule.Status != "waiting" && schedule.Status != "cycling" {
				continue
			}
			i.NextBackup = schedule.Next
			i.BackupState = "scheduled"
			if schedule.Status == "cycling" {
				if schedule.Current == i.ID {
					i.BackupState = "backing_up"
				} else {
					processed := false
					for _, id := range schedule.Processed {
						if id == i.ID {
							processed = true
						}
					}
					if !processed {
						i.BackupState = "queued"
					}
				}
			}
		}
		counts := map[string]int{}
		for _, i := range row.Instances {
			counts[i.Runtime]++
		}
		for rt, count := range counts {
			if count > 1 {
				row.Conflicts = append(row.Conflicts, rt+": duplicate Logic ID")
			}
		}
		row.selectCurrentInstances(result.Errors)
		row.ID = names[row.OriginNode+"/"+row.OriginID]
		result.Logics = append(result.Logics, *row)
	}
	sort.Slice(result.Logics, func(i, j int) bool { return result.Logics[i].ID < result.Logics[j].ID })
	return result, nil
}

// Instances retains all observations for conflict detection and safe restore checks.
// CurrentInstances is the ownership-bound projection displayed by the unified UI.
func (row *logic) selectCurrentInstances(runtimeErrors map[string]string) {
	row.CurrentInstances = make([]instance, 0, 1)
	row.CurrentState = "unassigned"
	if row.Owner != nil {
		if row.Owner.Instance == "" {
			row.CurrentState = "awaiting_restore"
			return
		}
		row.CurrentState = "missing"
		if runtimeErrors[row.Owner.Node] != "" {
			row.CurrentState = "unknown"
		}
		for _, i := range row.Instances {
			if i.Runtime == row.Owner.Node && i.ID == row.Owner.Instance {
				row.CurrentInstances = append(row.CurrentInstances, i)
				row.CurrentState = i.State
			}
		}
		return
	}
	// New instances can be displayed before the first backup creates ownership.
	for _, i := range row.Instances {
		if i.Runtime == row.OriginNode && i.ID == row.OriginID {
			row.CurrentInstances = append(row.CurrentInstances, i)
			row.CurrentState = i.State
		}
	}
}
