package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This live test runs as root in a fresh runtime2 guest, never against runtime1.
func TestRestoreLatestLive(t *testing.T) {
	if os.Getenv("RESTORE_LIVE") != "1" {
		t.Skip("set RESTORE_LIVE=1 on fresh runtime2")
	}
	c, e := configInit()
	if e != nil {
		t.Fatal(e)
	}
	if c.Node != "runtime2" {
		t.Fatal("restore test restricted to runtime2")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	client := s3.NewFromConfig(aws.Config{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AK, c.SK, "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.Endpoint); o.UsePathStyle = true })
	prefix := env("RESTORE_SOURCE_PREFIX", "backups/runtime1/i3boc9qul6uff777pdd2q/filesystem/")
	if !strings.HasPrefix(prefix, "backups/runtime1/") || !strings.HasSuffix(prefix, "/filesystem/") {
		t.Fatal("invalid source prefix")
	}
	var latest string
	p := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(c.Bucket), Prefix: aws.String(prefix)})
	for p.HasMorePages() {
		page, e := p.NextPage(ctx)
		if e != nil {
			t.Fatalf("LIST failed (%T)", e)
		}
		for _, o := range page.Contents {
			k := aws.ToString(o.Key)
			if strings.HasSuffix(k, ".tar.gz") && k > latest {
				latest = k
			}
		}
	}
	if latest == "" {
		t.Fatal("no backup")
	}
	stage, e := os.MkdirTemp(filepath.Dir(c.Storage), "restore-stage-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(stage)
	im := importer{client: client, bucket: c.Bucket, stage: stage, seen: map[string]bool{}}
	start := time.Now()
	manifest, e := im.download(ctx, latest)
	if e != nil {
		t.Fatal(e)
	}
	downloadMS := float64(time.Since(start).Microseconds()) / 1000
	// Ensure the imported metadata confirms a disk-only build and compatible binaries.
	raw, e := os.ReadFile(filepath.Join(stage, manifest.Record.BuildID, "metadata.json"))
	if e != nil {
		t.Fatal(e)
	}
	var parsed struct {
		FilesystemOnly bool  `json:"filesystem_only"`
		VCPUs          int64 `json:"vcpu_count"`
		Template       struct {
			Build  string `json:"build_id"`
			Kernel string `json:"kernel_version"`
			FC     string `json:"firecracker_version"`
		} `json:"template"`
	}
	if e = json.Unmarshal(raw, &parsed); e != nil {
		t.Fatal(e)
	}
	if !parsed.FilesystemOnly || parsed.Template.Build != manifest.Record.BuildID {
		t.Fatal("invalid snapshot metadata")
	}
	// This validation uses the same default base configuration as the demo source.
	// RAM/envd/CPU model come from the fresh base row; arbitrary-sized sandboxes
	// require their original configuration to be included in the backup manifest.
	sql := func(query string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", "compose", "exec", "-T", "postgres", "psql", "-U", "postgres", "-d", "postgres", "-At", "-v", "ON_ERROR_STOP=1")
		cmd.Dir = "/opt/e2b-runtime"
		cmd.Stdin = strings.NewReader(query)
		out, err := cmd.Output()
		if err != nil {
			t.Fatal("database registration/query failed")
		}
		return strings.TrimSpace(string(out))
	}
	var baseline struct {
		CPU    int64  `json:"cpu"`
		RAM    int64  `json:"ram"`
		Kernel string `json:"kernel"`
		FC     string `json:"fc"`
	}
	baselineJSON := sql(`SELECT json_build_object('cpu',b.vcpu,'ram',b.ram_mb,'kernel',b.kernel_version,'fc',b.firecracker_version) FROM env_builds b JOIN env_build_assignments a ON a.build_id=b.id JOIN env_aliases x ON x.env_id=a.env_id WHERE x.alias='base' AND b.status_group='ready' ORDER BY b.created_at DESC LIMIT 1;`)
	if e = json.Unmarshal([]byte(baselineJSON), &baseline); e != nil {
		t.Fatal("base row not found")
	}
	if baseline.Kernel != parsed.Template.Kernel || baseline.FC != parsed.Template.FC || (parsed.VCPUs != 0 && parsed.VCPUs != baseline.CPU) {
		t.Fatal("base configuration/version mismatch")
	}
	for _, id := range manifest.Builds {
		if !uuidPattern.MatchString(id) {
			t.Fatal("invalid build")
		}
		if _, e = os.Stat(filepath.Join(c.Storage, id)); !os.IsNotExist(e) {
			t.Fatal("target build already exists")
		}
		if e = os.Rename(filepath.Join(stage, id), filepath.Join(c.Storage, id)); e != nil {
			t.Fatal(e)
		}
	}
	nonce := make([]byte, 8)
	if _, e = rand.Read(nonce); e != nil {
		t.Fatal(e)
	}
	template := "restore" + hex.EncodeToString(nonce)
	build := manifest.Record.BuildID
	// COPY only configuration fields from an existing authorized base; new UUID
	// and template assignment point exclusively at the S3-imported rootfs build.
	query := fmt.Sprintf(`BEGIN;
 CREATE TEMP TABLE restore_base ON COMMIT DROP AS SELECT b.* FROM env_builds b JOIN env_build_assignments a ON a.build_id=b.id JOIN env_aliases x ON x.env_id=a.env_id WHERE x.alias='base' AND b.status_group='ready' ORDER BY b.created_at DESC LIMIT 1;
 INSERT INTO envs(id,team_id,public,source,updated_at) SELECT '%s',team_id,false,'template',now() FROM restore_base;
 INSERT INTO env_builds(id,env_id,team_id,status,ram_mb,vcpu,kernel_version,firecracker_version,envd_version,free_disk_size_mb,total_disk_size_mb,version,cpu_architecture,cpu_family,cpu_model,cpu_model_name,cpu_flags,updated_at,finished_at) SELECT '%s','%s',team_id,status,ram_mb,vcpu,kernel_version,firecracker_version,envd_version,free_disk_size_mb,total_disk_size_mb,version,cpu_architecture,cpu_family,cpu_model,cpu_model_name,cpu_flags,now(),now() FROM restore_base;
 INSERT INTO env_build_assignments(env_id,build_id,tag) VALUES('%s','%s','default');
 COMMIT;`, template, build, template, template, build)
	sql(query)
	a := newAPI(c)
	var sb sandbox
	start = time.Now()
	_, e = a.call(ctx, "POST", "/v2/sandboxes", map[string]any{"templateID": template, "timeout": 1800, "metadata": map[string]string{"purpose": "restored-from-runtime1"}}, &sb)
	if e != nil {
		t.Fatal(e)
	}
	restoreMS := float64(time.Since(start).Microseconds()) / 1000
	result, e := a.RunCommand(ctx, sb, "cat /home/user/backup-demo.log")
	if e != nil {
		t.Fatal(e)
	}
	content := strings.TrimSpace(result.Stdout)
	lines := strings.Split(content, "\n")
	if len(lines) < 2 {
		t.Fatal("backup demo file missing history")
	}
	last, e := time.Parse(time.RFC3339, lines[len(lines)-1])
	if e != nil {
		t.Fatal("bad restored timestamp")
	}
	backupTime, e := time.Parse(time.RFC3339Nano, im.files[0].BackupAt)
	if e != nil {
		t.Fatal(e)
	}
	if last.After(backupTime.Add(time.Second)) || backupTime.Sub(last) > time.Minute {
		t.Fatal("restored file is not from selected latest backup")
	}
	boot, e := a.RunCommand(ctx, sb, "cat /proc/sys/kernel/random/boot_id; uname -s")
	if e != nil {
		t.Fatal(e)
	}
	report := map[string]any{"source_key": latest, "files": im.files, "template_id": template, "sandbox_id": sb.ID, "download_extract_verify_ms": downloadMS, "create_ms": restoreMS, "restored_log_lines": len(lines), "restored_last_timestamp": last.Format(time.RFC3339), "guest": strings.TrimSpace(boot.Stdout), "cpu": baseline.CPU, "memory_mb": baseline.RAM, "file_verified": true, "filesystem_cold_boot": true, "passed": true, "note": "default-base filesystem recovery; does not recover running processes/memory"}
	data, _ := json.MarshalIndent(report, "", "  ")
	out := env("RESTORE_REPORT", "/opt/e2b-runtime/restore-result.json")
	if e = os.WriteFile(out, append(data, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	t.Logf("restored sandbox=%s selected=%s files=%d log_lines=%d last=%s report=%s", sb.ID, latest, len(im.files), len(lines), last.Format(time.RFC3339), out)
}
