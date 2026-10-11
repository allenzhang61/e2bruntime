package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

type importResult struct {
	SourceSandboxID string   `json:"source_sandbox_id"`
	ArchiveKey      string   `json:"archive_key"`
	Status          string   `json:"status"`
	SandboxID       string   `json:"sandbox_id,omitempty"`
	TemplateID      string   `json:"template_id,omitempty"`
	Error           string   `json:"error,omitempty"`
	DurationMS      float64  `json:"duration_ms"`
	CurrentState    string   `json:"current_state,omitempty"`
	MissingFields   []string `json:"missing_fields,omitempty"`
}
type importJob struct {
	ID        string         `json:"id"`
	Status    string         `json:"status"`
	CreatedAt string         `json:"created_at_utc"`
	Results   []importResult `json:"results"`
}
type importManager struct {
	w      *worker
	mu     sync.Mutex
	active string
}

func newImportManager(w *worker) *importManager { return &importManager{w: w} }
func saveJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".import-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
func (m *importManager) path(id string) string { return filepath.Join(m.w.cfg.ImportJobs, id+".json") }
func (m *importManager) read(id string) (importJob, error) {
	var j importJob
	data, err := os.ReadFile(m.path(id))
	if err != nil {
		return j, err
	}
	err = json.Unmarshal(data, &j)
	if err == nil && (j.Status == "running" || j.Status == "queued") && m.active != id {
		j.Status = "interrupted"
	}
	return j, err
}
func (m *importManager) handle(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	if r.Method == "GET" {
		id := r.URL.Query().Get("id")
		if len(id) != 64 {
			http.Error(rw, "invalid job ID", 400)
			return
		}
		if _, err := hex.DecodeString(id); err != nil {
			http.Error(rw, "invalid job ID", 400)
			return
		}
		m.mu.Lock()
		j, err := m.read(id)
		m.mu.Unlock()
		if err != nil {
			http.Error(rw, "job not found", 404)
			return
		}
		m.refreshCurrentState(r.Context(), &j)
		json.NewEncoder(rw).Encode(j)
		return
	}
	if r.Method != "POST" {
		http.Error(rw, "GET or POST required", 405)
		return
	}
	var entries []transferEntry
	dec := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&entries); err != nil || len(entries) == 0 || len(entries) > 100 {
		http.Error(rw, "expected JSON Array with 1–100 backup entries (max 8 MiB)", 400)
		return
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		http.Error(rw, "unexpected trailing JSON", 400)
		return
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if err := m.w.validateTransfer(e); err != nil || seen[e.SourceNode+"/"+e.SandboxID] {
			http.Error(rw, "invalid, unsupported or duplicate backup entry", 400)
			return
		}
		seen[e.SourceNode+"/"+e.SandboxID] = true
	}
	raw, _ := json.Marshal(entries)
	sum := sha256.Sum256(raw)
	id := hex.EncodeToString(sum[:])
	m.mu.Lock()
	defer m.mu.Unlock()
	if j, err := m.read(id); err == nil && j.Status != "completed_with_errors" {
		m.refreshCurrentState(r.Context(), &j)
		for _, result := range j.Results {
			if result.SandboxID != "" && result.CurrentState != "running" && result.CurrentState != "paused" {
				http.Error(rw, "Historical import exists, but its instance is missing or cannot be verified. No new instance was created; inspect the history and use a new backup for another import.", 409)
				return
			}
		}
		json.NewEncoder(rw).Encode(j)
		return
	}
	if m.active != "" {
		http.Error(rw, "another import job is running", 409)
		return
	}
	j := importJob{ID: id, Status: "queued", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Results: make([]importResult, len(entries))}
	for i, e := range entries {
		j.Results[i] = importResult{SourceSandboxID: e.SandboxID, ArchiveKey: e.ArchiveKey, Status: "queued"}
	}
	if err := saveJSON(m.path(id), j); err != nil {
		http.Error(rw, "unable to persist import job", 500)
		return
	}
	m.active = id
	go m.run(j, entries)
	rw.WriteHeader(202)
	json.NewEncoder(rw).Encode(j)
}
func (m *importManager) run(j importJob, entries []transferEntry) {
	defer func() { m.mu.Lock(); m.active = ""; m.mu.Unlock() }()
	publish := func() bool { m.mu.Lock(); defer m.mu.Unlock(); return saveJSON(m.path(j.ID), j) == nil }
	j.Status = "running"
	if !publish() {
		return
	}
	failed := false
	for index, e := range entries {
		j.Results[index].Status = "running"
		if !publish() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		var result importResult
		err := m.w.withNodeLock(ctx, func() error { result = m.w.importEntry(ctx, e); return nil })
		cancel()
		if err != nil {
			result = importResult{SourceSandboxID: e.SandboxID, ArchiveKey: e.ArchiveKey, Status: "failed", Error: err.Error()}
		}
		if result.Status != "succeeded" && result.Status != "existing" {
			failed = true
		}
		j.Results[index] = result
		if !publish() {
			return
		}
	}
	j.Status = "completed"
	if failed {
		j.Status = "completed_with_errors"
	}
	publish()
}
func (w *worker) importEntry(ctx context.Context, e transferEntry) (result importResult) {
	start := time.Now()
	result = importResult{SourceSandboxID: e.SandboxID, ArchiveKey: e.ArchiveKey, Status: "failed"}
	defer func() { result.DurationMS = float64(time.Since(start).Microseconds()) / 1000 }()
	fail := func(err error) importResult { result.Status = "failed"; result.Error = err.Error(); return result }
	m, err := w.readRecovery(ctx, e.ArchiveKey)
	if err != nil {
		return fail(err)
	}
	if !metadataEqual(m, e.Metadata) {
		return fail(fmt.Errorf("pasted Metadata differs from verified S3 Metadata"))
	}
	result.MissingFields = m.MissingFields
	if m.Sandbox.CPUCount < 1 || m.Sandbox.CPUCount > 64 || m.Sandbox.MemoryMB < 128 || m.Sandbox.MemoryMB > 262144 || m.Sandbox.DiskSizeMB < 1 {
		return fail(fmt.Errorf("unsupported CPU/RAM/disk configuration"))
	}
	if len(m.Sandbox.VolumeMounts) > 0 {
		return fail(fmt.Errorf("external volumes require separate migration"))
	}
	for _, field := range m.MissingFields {
		if strings.HasPrefix(field, "network.") {
			return fail(fmt.Errorf("omitted network policy cannot be safely restored"))
		}
	}
	archiveSum := sha256.Sum256([]byte(e.Bucket + "/" + e.ArchiveKey))
	ledgerSum := archiveSum
	if e.OwnershipGeneration > 0 {
		ledgerSum = sha256.Sum256([]byte(fmt.Sprintf("%s/%s/generation/%d", e.Bucket, e.ArchiveKey, e.OwnershipGeneration)))
	}
	ledger := filepath.Join(w.cfg.ImportJobs, "archives", hex.EncodeToString(ledgerSum[:])+".json")
	if raw, err := os.ReadFile(ledger); err == nil {
		var previous importResult
		if json.Unmarshal(raw, &previous) != nil {
			return fail(fmt.Errorf("invalid prior import record"))
		}
		if previous.Status == "succeeded" {
			n, s, err := logicalIdentity(e.SourceNode, e.SandboxID, m.Sandbox.Metadata)
			if err != nil {
				return fail(err)
			}
			owner, _, err := w.readOwner(ctx, n, s)
			if err != nil || owner.OwnerNode != w.cfg.Node || owner.OwnerSandboxID != previous.SandboxID || owner.State != "active" {
				return fail(fmt.Errorf("existing import no longer owned by this Runtime"))
			}
			previous.Status = "existing"
			return previous
		}
		return fail(fmt.Errorf("previous attempt requires inspection before retry (possible partial creation)"))
	} else if !os.IsNotExist(err) {
		return fail(fmt.Errorf("unable to read import record"))
	}
	logicalNode, logicalID, err := logicalIdentity(e.SourceNode, e.SandboxID, m.Sandbox.Metadata)
	if err != nil {
		return fail(err)
	}
	if err = w.requireLogicalVacancy(ctx, logicalNode, logicalID); err != nil {
		return fail(err)
	}
	owner, ownerETag, err := w.authorizeImport(ctx, e)
	if err != nil {
		return fail(err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(w.cfg.Storage), "admin-import-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(stage)
	im := importer{client: w.s3, bucket: w.cfg.Bucket, stage: stage, sourceNode: e.SourceNode, seen: map[string]bool{}}
	manifest, err := im.download(ctx, e.ArchiveKey)
	if err != nil {
		return fail(err)
	}
	if manifest.Record.BuildID != m.BuildID || len(im.files) == 0 || im.files[0].SHA256 != m.ArchiveSHA256 || im.files[0].Bytes != m.ArchiveBytes || !reflect.DeepEqual(manifest.External, m.Dependencies) {
		return fail(fmt.Errorf("archive does not match recovery Metadata"))
	}
	raw, err := os.ReadFile(filepath.Join(stage, m.BuildID, "metadata.json"))
	if err != nil {
		return fail(err)
	}
	var native struct {
		FilesystemOnly bool `json:"filesystem_only"`
		VCPUs          int  `json:"vcpu_count"`
		Template       struct {
			Build  string `json:"build_id"`
			Kernel string `json:"kernel_version"`
			FC     string `json:"firecracker_version"`
		} `json:"template"`
	}
	if json.Unmarshal(raw, &native) != nil || !native.FilesystemOnly || native.Template.Build != m.BuildID || (native.VCPUs != 0 && native.VCPUs != m.Sandbox.CPUCount) {
		return fail(fmt.Errorf("invalid native filesystem snapshot"))
	}
	sql := func(query string) (string, error) {
		cmd := exec.CommandContext(ctx, "docker", "compose", "-f", "compose.yaml", "exec", "-T", "postgres", "psql", "-U", "postgres", "-d", "postgres", "-At", "-v", "ON_ERROR_STOP=1")
		cmd.Dir = "/opt/e2b-runtime"
		cmd.Stdin = strings.NewReader(query)
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("target database registration/query failed")
		}
		return strings.TrimSpace(string(out)), nil
	}
	keyBytes, err := hex.DecodeString(strings.TrimPrefix(w.cfg.Key, "e2b_"))
	if err != nil || !strings.HasPrefix(w.cfg.Key, "e2b_") {
		return fail(fmt.Errorf("unsupported target team key format"))
	}
	keyHash := sha256.Sum256(keyBytes)
	hashed := "$sha256$" + base64.RawStdEncoding.EncodeToString(keyHash[:])
	var baseline struct {
		CPU    int    `json:"cpu"`
		RAM    int    `json:"ram"`
		Disk   int    `json:"disk"`
		Kernel string `json:"kernel"`
		FC     string `json:"fc"`
		Envd   string `json:"envd"`
		Team   string `json:"team"`
	}
	baseQuery := `SELECT b.* FROM env_builds b JOIN env_build_assignments a ON a.build_id=b.id JOIN env_aliases x ON x.env_id=a.env_id WHERE x.alias='base' AND b.status_group='ready' ORDER BY b.created_at DESC LIMIT 1`
	baselineJSON, err := sql(`SELECT json_build_object('cpu',b.vcpu,'ram',b.ram_mb,'disk',b.total_disk_size_mb,'kernel',b.kernel_version,'fc',b.firecracker_version,'envd',b.envd_version,'team',k.team_id) FROM (` + baseQuery + `) b CROSS JOIN team_api_keys k WHERE k.api_key_hash='` + hashed + `';`)
	if err != nil || json.Unmarshal([]byte(baselineJSON), &baseline) != nil || !uuidPattern.MatchString(baseline.Team) {
		return fail(fmt.Errorf("authorized target team/base template unavailable"))
	}
	if baseline.Kernel != native.Template.Kernel || baseline.FC != native.Template.FC || baseline.Envd != m.Sandbox.EnvdVersion || baseline.Disk != m.Sandbox.DiskSizeMB {
		return fail(fmt.Errorf("target base kernel/Firecracker/envd/disk configuration incompatible"))
	}
	// Publish only missing immutable builds; shared layers may already exist.
	for _, id := range manifest.Builds {
		if !uuidPattern.MatchString(id) {
			return fail(fmt.Errorf("invalid build ID"))
		}
		src, dst := filepath.Join(stage, id), filepath.Join(w.cfg.Storage, id)
		if _, err = os.Lstat(dst); os.IsNotExist(err) {
			if err = os.Rename(src, dst); err != nil {
				return fail(err)
			}
		} else if err != nil {
			return fail(err)
		} else if err = compareBuild(src, dst); err != nil {
			return fail(err)
		}
	}
	// A deleted template is a permanent tombstone, even when its immutable build
	// remains available. Use a new recovery template for this lifecycle instead.
	fallback := "restore" + hex.EncodeToString(ledgerSum[:10])
	existingJSON, err := sql("SELECT json_build_object('template',b.env_id,'team',b.team_id,'public',e.public,'deleted',e.deleted_at IS NOT NULL) FROM env_builds b JOIN envs e ON e.id=b.env_id WHERE b.id='" + m.BuildID + "';")
	if err != nil {
		return fail(err)
	}
	template, err := selectRestoreTemplate(existingJSON, baseline.Team, fallback)
	if err != nil {
		return fail(err)
	}
	result.TemplateID = template
	// Reserving the archive before DB/API mutation prevents duplicate creation after lost responses.
	owner.State = "importing"
	owner.Generation++
	ownerETag, err = w.writeOwner(ctx, owner, ownerETag)
	if err != nil {
		return fail(err)
	}
	result.Status = "creating"
	if err = saveJSON(ledger, result); err != nil {
		return fail(err)
	}
	query := fmt.Sprintf(`BEGIN;
 CREATE TEMP TABLE restore_base ON COMMIT DROP AS %s;
 INSERT INTO envs(id,team_id,public,source,updated_at) VALUES('%s','%s',false,'template',now()) ON CONFLICT DO NOTHING;
 INSERT INTO env_builds(id,env_id,team_id,status,ram_mb,vcpu,kernel_version,firecracker_version,envd_version,free_disk_size_mb,total_disk_size_mb,version,cpu_architecture,cpu_family,cpu_model,cpu_model_name,cpu_flags,updated_at,finished_at) SELECT '%s','%s','%s',status,%d,%d,kernel_version,firecracker_version,envd_version,free_disk_size_mb,total_disk_size_mb,version,cpu_architecture,cpu_family,cpu_model,cpu_model_name,cpu_flags,now(),now() FROM restore_base ON CONFLICT DO NOTHING;
 INSERT INTO env_build_assignments(env_id,build_id,tag) SELECT '%s','%s','default' WHERE NOT EXISTS(SELECT 1 FROM env_build_assignments WHERE env_id='%s' AND build_id='%s' AND tag='default') AND EXISTS(SELECT 1 FROM env_builds WHERE id='%s' AND team_id='%s') ON CONFLICT DO NOTHING;
 COMMIT; SELECT count(DISTINCT a.build_id) FROM env_build_assignments a JOIN active_envs e ON e.id=a.env_id JOIN env_builds b ON b.id=a.build_id WHERE e.id='%s' AND e.team_id='%s' AND a.build_id='%s' AND b.team_id=e.team_id AND b.status_group='ready';`, baseQuery, template, baseline.Team, m.BuildID, template, baseline.Team, m.Sandbox.MemoryMB, m.Sandbox.CPUCount, template, m.BuildID, template, m.BuildID, m.BuildID, baseline.Team, template, baseline.Team, m.BuildID)
	registered, err := sql(query)
	if err != nil || !strings.HasSuffix(registered, "\n1") {
		// No create API call has occurred; this generation can safely be retried.
		owner.State = "failed_before_create"
		owner.Generation++
		_, _ = w.writeOwner(ctx, owner, ownerETag)
		return fail(fmt.Errorf("target template registration conflict or failure"))
	}
	restoredMetadata := map[string]string{}
	for k, v := range m.Sandbox.Metadata {
		restoredMetadata[k] = v
	}
	restoredMetadata["dev_backup_origin_node"] = owner.LogicalNode
	restoredMetadata["dev_backup_origin_id"] = owner.LogicalSandboxID
	payload := map[string]any{"templateID": template + ":" + m.BuildID, "timeout": 1800, "metadata": restoredMetadata}
	if m.Sandbox.AllowInternetAccess != nil {
		payload["allow_internet_access"] = *m.Sandbox.AllowInternetAccess
	}
	if m.Sandbox.Network != nil {
		payload["network"] = m.Sandbox.Network
	}
	var lifecycle struct {
		AutoResume bool   `json:"autoResume"`
		OnTimeout  string `json:"onTimeout"`
	}
	if len(m.Sandbox.Lifecycle) > 0 && json.Unmarshal(m.Sandbox.Lifecycle, &lifecycle) != nil {
		return fail(fmt.Errorf("invalid lifecycle configuration"))
	}
	payload["autoPause"] = lifecycle.OnTimeout == "pause"
	payload["autoResume"] = map[string]bool{"enabled": lifecycle.AutoResume}
	var sb sandbox
	_, err = w.api.call(ctx, "POST", "/v2/sandboxes", payload, &sb)
	if err != nil {
		return fail(fmt.Errorf("create failed; inspect target before retry: %w", err))
	}
	if !sandboxIDPattern.MatchString(sb.ID) {
		return fail(fmt.Errorf("create response missing sandbox ID; inspect target"))
	}
	result.SandboxID = sb.ID
	result.Status = "created"
	if err = saveJSON(ledger, result); err != nil {
		return fail(fmt.Errorf("sandbox created but result persistence failed: %s", sb.ID))
	}
	_, err = w.api.RunCommand(ctx, sb, "test -d /home/user && uname -s")
	if err != nil {
		return fail(fmt.Errorf("sandbox created but guest check failed; retained sandbox %s", sb.ID))
	}
	owner.OwnerSandboxID = sb.ID
	owner.State = "active"
	owner.Generation++
	if _, err = w.writeOwner(ctx, owner, ownerETag); err != nil {
		return fail(fmt.Errorf("sandbox created but backup ownership activation failed: %s", sb.ID))
	}
	result.Status = "succeeded"
	result.DurationMS = float64(time.Since(start).Microseconds()) / 1000
	if err = saveJSON(ledger, result); err != nil {
		return fail(fmt.Errorf("sandbox created; unable to finalize import record: %s", sb.ID))
	}
	return result
}
func compareBuild(src, dst string) error {
	info, err := os.Lstat(dst)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("unsafe existing build directory")
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		a, b := filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())
		ai, err := os.Stat(a)
		if err != nil {
			return err
		}
		bi, err := os.Lstat(b)
		if err != nil || !bi.Mode().IsRegular() || ai.Size() != bi.Size() {
			return fmt.Errorf("existing build collision")
		}
		digest := func(path string) (string, error) {
			f, err := os.Open(path)
			if err != nil {
				return "", err
			}
			defer f.Close()
			h := sha256.New()
			_, err = io.Copy(h, f)
			return hex.EncodeToString(h.Sum(nil)), err
		}
		ah, err := digest(a)
		if err != nil {
			return err
		}
		bh, err := digest(b)
		if err != nil || ah != bh {
			return fmt.Errorf("existing build content mismatch")
		}
	}
	return nil
}

func metadataEqual(m recoveryMetadata, pasted *recoveryMetadata) bool {
	a, _ := json.Marshal(m)
	b, _ := json.Marshal(pasted)
	var av, bv any
	return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil && reflect.DeepEqual(av, bv)
}

// Live state is response-only; a historical successful import remains an audit record.
func (m *importManager) refreshCurrentState(parent context.Context, job *importJob) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	for i := range job.Results {
		if job.Results[i].SandboxID != "" {
			job.Results[i].CurrentState = m.w.api.sandboxState(ctx, job.Results[i].SandboxID)
		}
	}
}

// selectRestoreTemplate never resurrects a soft-deleted source template, and
// validates ownership even when choosing a new template for an existing build.
func selectRestoreTemplate(raw, team, fallback string) (string, error) {
	if raw == "" {
		return fallback, nil
	}
	var existing struct {
		Template string `json:"template"`
		Team     string `json:"team"`
		Public   bool   `json:"public"`
		Deleted  *bool  `json:"deleted"`
	}
	if json.Unmarshal([]byte(raw), &existing) != nil || existing.Team != team || existing.Public || existing.Deleted == nil || !sandboxIDPattern.MatchString(existing.Template) {
		return "", fmt.Errorf("existing snapshot registration is not a private template owned by this team")
	}
	if *existing.Deleted {
		return fallback, nil
	}
	return existing.Template, nil
}
