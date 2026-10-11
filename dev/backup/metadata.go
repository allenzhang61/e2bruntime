package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
	"net/url"
	"strings"
	"time"
)

// Explicit fields prevent API/guest access tokens from entering the archive.
type sandboxConfig struct {
	SandboxID           string            `json:"sandboxID"`
	TemplateID          string            `json:"templateID"`
	CPUCount            int               `json:"cpuCount"`
	MemoryMB            int               `json:"memoryMB"`
	DiskSizeMB          int               `json:"diskSizeMB"`
	EnvdVersion         string            `json:"envdVersion"`
	State               string            `json:"state"`
	StartedAt           string            `json:"startedAt"`
	EndAt               string            `json:"endAt"`
	Metadata            map[string]string `json:"metadata"`
	AllowInternetAccess *bool             `json:"allowInternetAccess"`
	Lifecycle           json.RawMessage   `json:"lifecycle"`
	VolumeMounts        []struct {
		Name string `json:"name"`
		Path string `json:"path"`
	} `json:"volumeMounts"`
	Network *networkConfig `json:"network"`
}
type networkConfig struct {
	AllowPublicTraffic *bool    `json:"allowPublicTraffic"`
	AllowOut           []string `json:"allowOut"`
	DenyOut            []string `json:"denyOut"`
	MaskRequestHost    string   `json:"maskRequestHost"`
	HTTPSPorts         []uint32 `json:"httpsPorts"`
	// Presence is recorded; transform rules/proxy definitions may carry credentials.
	Rules       json.RawMessage `json:"rules,omitempty"`
	EgressProxy json.RawMessage `json:"egressProxy,omitempty"`
}
type recoveryMetadata struct {
	ArchiveKey    string            `json:"archive_key"`
	ArchiveSHA256 string            `json:"archive_sha256"`
	ArchiveBytes  int64             `json:"archive_bytes"`
	Dependencies  map[string]string `json:"external_build_archives"`
	SchemaVersion int               `json:"schema_version"`
	SourceNode    string            `json:"source_node"`
	SnapshotID    string            `json:"snapshot_id"`
	BuildID       string            `json:"build_id"`
	CapturedAt    string            `json:"captured_at_utc"`
	Sandbox       sandboxConfig     `json:"sandbox"`
	MissingFields []string          `json:"missing_fields"`
}

func (a *api) configuration(ctx context.Context, id string) (sandboxConfig, error) {
	var c sandboxConfig
	_, e := a.call(ctx, "GET", "/sandboxes/"+url.PathEscape(id), nil, &c)
	if e != nil {
		return c, e
	}
	if c.SandboxID != id || c.TemplateID == "" || c.CPUCount < 1 || c.MemoryMB < 1 || c.DiskSizeMB < 1 || c.State != "running" {
		return c, fmt.Errorf("invalid or non-running sandbox configuration")
	}
	return c, nil
}
func configurationStable(a, b sandboxConfig) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func makeRecoveryMetadata(c sandboxConfig, node, snapshot, build string) ([]byte, error) {
	missing := []string{"envVars", "team_id/user_ownership", "agent_start_command", "autoPauseMemory", "iam"}
	if c.Network != nil {
		copy := *c.Network
		c.Network = &copy
		if len(copy.Rules) > 0 && string(copy.Rules) != "null" && string(copy.Rules) != "{}" {
			missing = append(missing, "network.rules (credential-bearing)")
		}
		if len(copy.EgressProxy) > 0 && string(copy.EgressProxy) != "null" {
			missing = append(missing, "network.egressProxy (credential-bearing)")
		}
		c.Network.Rules = nil
		c.Network.EgressProxy = nil
	}
	return json.MarshalIndent(recoveryMetadata{SchemaVersion: 1, SourceNode: node, SnapshotID: snapshot, BuildID: build, CapturedAt: time.Now().UTC().Format(time.RFC3339Nano), Sandbox: c, MissingFields: missing}, "", "  ")
}

func metadataKey(archive string) string {
	return strings.TrimSuffix(archive, ".tar.gz") + ".metadata.json"
}
func (w *worker) uploadMetadata(ctx context.Context, key string, size int64, sha string, raw []byte, deps map[string]string) (int, error) {
	var m recoveryMetadata
	if e := json.Unmarshal(raw, &m); e != nil {
		return 0, e
	}
	m.ArchiveKey = key
	m.ArchiveSHA256 = sha
	m.ArchiveBytes = size
	m.Dependencies = deps
	data, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return 0, e
	}
	if len(data) > 1<<20 {
		return 0, fmt.Errorf("metadata exceeds 1 MiB")
	}
	hash := sha256.Sum256(data)
	metadataSHA := hex.EncodeToString(hash[:])
	_, e = w.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(metadataKey(key)), Body: bytes.NewReader(data), ContentType: aws.String("application/json"), Metadata: map[string]string{"sha256": metadataSHA, "archive-key": key, "kind": "filesystem-metadata"}})
	if e != nil {
		return 0, fmt.Errorf("metadata S3 PUT failed (%T)", e)
	}
	got, e := w.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(metadataKey(key))})
	if e != nil {
		return 0, fmt.Errorf("metadata S3 GET failed (%T)", e)
	}
	defer got.Body.Close()
	actual, e := io.ReadAll(io.LimitReader(got.Body, int64(len(data))+1))
	if e != nil || !bytes.Equal(actual, data) || got.Metadata["sha256"] != metadataSHA || aws.ToInt64(got.ContentLength) != int64(len(data)) {
		return 0, fmt.Errorf("metadata download verification failed")
	}
	return len(data), nil
}
