package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"io"
	"net/http"
	"strings"
	"time"
)

var errNotBackupOwner = errors.New("backup ownership belongs to another Runtime or is awaiting import")

type backupOwnership struct {
	SchemaVersion    int    `json:"schema_version"`
	LogicalNode      string `json:"logical_node"`
	LogicalSandboxID string `json:"logical_sandbox_id"`
	OwnerNode        string `json:"owner_node"`
	OwnerSandboxID   string `json:"owner_sandbox_id"`
	State            string `json:"state"`
	Generation       int64  `json:"generation"`
	PinnedArchive    string `json:"pinned_archive,omitempty"`
	UpdatedAt        string `json:"updated_at_utc"`
}

func ownershipKey(node, id string) string {
	return "control/backup-owners/" + node + "/" + id + ".json"
}
func logicalIdentity(node, id string, metadata map[string]string) (string, string, error) {
	n, s := metadata["dev_backup_origin_node"], metadata["dev_backup_origin_id"]
	if n == "" && s == "" {
		return node, id, nil
	}
	if !sandboxIDPattern.MatchString(n) || strings.Contains(n, ".") || !sandboxIDPattern.MatchString(s) {
		return "", "", fmt.Errorf("invalid backup lineage")
	}
	return n, s, nil
}
func (w *worker) readOwner(ctx context.Context, node, id string) (backupOwnership, string, error) {
	var owner backupOwnership
	got, err := w.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(ownershipKey(node, id))})
	if err != nil {
		return owner, "", err
	}
	defer got.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(got.Body, 16385))
	if err != nil || len(raw) > 16384 || json.Unmarshal(raw, &owner) != nil || owner.SchemaVersion != 1 || owner.LogicalNode != node || owner.LogicalSandboxID != id || owner.Generation < 1 || aws.ToString(got.ETag) == "" {
		return owner, "", fmt.Errorf("invalid ownership record")
	}
	return owner, aws.ToString(got.ETag), nil
}
func (w *worker) writeOwner(ctx context.Context, owner backupOwnership, etag string) (string, error) {
	owner.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.MarshalIndent(owner, "", "  ")
	if err != nil {
		return "", err
	}
	input := &s3.PutObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(ownershipKey(owner.LogicalNode, owner.LogicalSandboxID)), Body: bytes.NewReader(raw), ContentType: aws.String("application/json")}
	if etag == "" {
		input.IfNoneMatch = aws.String("*")
	} else {
		input.IfMatch = aws.String(etag)
	}
	out, err := w.s3.PutObject(ctx, input)
	if err != nil {
		return "", fmt.Errorf("ownership conditional write failed; re-read before retry")
	}
	if aws.ToString(out.ETag) == "" {
		return "", fmt.Errorf("ownership ETag unavailable")
	}
	return aws.ToString(out.ETag), nil
}
func (w *worker) backupPermission(ctx context.Context, c sandboxConfig) (backupOwnership, string, error) {
	node, id, err := logicalIdentity(w.cfg.Node, c.SandboxID, c.Metadata)
	if err != nil {
		return backupOwnership{}, "", err
	}
	owner, etag, err := w.readOwner(ctx, node, id)
	if err != nil {
		var api smithy.APIError
		if node != w.cfg.Node || id != c.SandboxID || !errors.As(err, &api) || (api.ErrorCode() != "NoSuchKey" && api.ErrorCode() != "NotFound") {
			return owner, "", fmt.Errorf("ownership lookup failed; backup denied")
		}
		owner = backupOwnership{SchemaVersion: 1, LogicalNode: node, LogicalSandboxID: id, OwnerNode: w.cfg.Node, OwnerSandboxID: c.SandboxID, State: "active", Generation: 1}
		if _, err = w.writeOwner(ctx, owner, ""); err != nil {
			return owner, "", err
		}
		owner, etag, err = w.readOwner(ctx, node, id)
		if err != nil {
			return owner, "", err
		}
	}
	if owner.OwnerNode != w.cfg.Node || owner.OwnerSandboxID != c.SandboxID || owner.State != "active" {
		return owner, etag, errNotBackupOwner
	}
	return owner, etag, nil
}
func (w *worker) checkGeneration(ctx context.Context, owner backupOwnership, etag string) error {
	latest, current, err := w.readOwner(ctx, owner.LogicalNode, owner.LogicalSandboxID)
	if err != nil || current != etag || latest.Generation != owner.Generation || latest.OwnerNode != w.cfg.Node || latest.OwnerSandboxID != owner.OwnerSandboxID || latest.State != "active" {
		return errNotBackupOwner
	}
	return nil
}
func (w *worker) authorizeImport(ctx context.Context, e transferEntry) (backupOwnership, string, error) {
	n, id, err := logicalIdentity(e.SourceNode, e.SandboxID, e.Metadata.Sandbox.Metadata)
	if err != nil {
		return backupOwnership{}, "", err
	}
	owner, etag, err := w.readOwner(ctx, n, id)
	if err != nil {
		return owner, "", fmt.Errorf("ownership record unavailable; import abandoned")
	}
	if owner.OwnerNode != w.cfg.Node || owner.State != "ready" || owner.PinnedArchive != e.ArchiveKey || owner.OwnerSandboxID != "" || (e.OwnershipGeneration != 0 && owner.Generation != e.OwnershipGeneration) {
		return owner, etag, fmt.Errorf("backup is not assigned to this Runtime; import abandoned")
	}
	return owner, etag, nil
}

type ownershipView struct {
	ArchiveKey     string           `json:"archive_key"`
	CurrentRuntime string           `json:"current_runtime"`
	Owner          *backupOwnership `json:"owner,omitempty"`
	Owned          bool             `json:"owned"`
	Error          string           `json:"error,omitempty"`
}

func (w *worker) ownershipView(ctx context.Context, e transferEntry) (ownershipView, error) {
	view := ownershipView{ArchiveKey: e.ArchiveKey, CurrentRuntime: w.cfg.Node}
	if err := w.validateTransfer(e); err != nil {
		return view, err
	}
	m, err := w.readRecovery(ctx, e.ArchiveKey)
	if err != nil {
		return view, err
	}
	n, id, err := logicalIdentity(e.SourceNode, e.SandboxID, m.Sandbox.Metadata)
	if err != nil {
		return view, err
	}
	owner, _, err := w.readOwner(ctx, n, id)
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && (api.ErrorCode() == "NoSuchKey" || api.ErrorCode() == "NotFound") {
			return view, nil
		}
		return view, fmt.Errorf("ownership unavailable")
	}
	view.Owner = &owner
	view.Owned = owner.OwnerNode == w.cfg.Node
	return view, nil
}
func (w *worker) takeover(ctx context.Context, e transferEntry) error {
	if err := w.validateTransfer(e); err != nil {
		return err
	}
	m, err := w.readRecovery(ctx, e.ArchiveKey)
	if err != nil {
		return err
	}
	n, id, err := logicalIdentity(e.SourceNode, e.SandboxID, m.Sandbox.Metadata)
	if err != nil {
		return err
	}
	owner, etag, err := w.readOwner(ctx, n, id)
	if err != nil {
		var api smithy.APIError
		if !errors.As(err, &api) || (api.ErrorCode() != "NoSuchKey" && api.ErrorCode() != "NotFound") {
			return fmt.Errorf("ownership unavailable; takeover denied")
		}
		owner = backupOwnership{SchemaVersion: 1, LogicalNode: n, LogicalSandboxID: id}
	}
	if owner.OwnerNode == w.cfg.Node {
		return fmt.Errorf("already owned by this Runtime; takeover is unnecessary")
	}
	head, err := w.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(e.ArchiveKey)})
	if err != nil || aws.ToInt64(head.ContentLength) != m.ArchiveBytes || head.Metadata["sha256"] != m.ArchiveSHA256 {
		return fmt.Errorf("archive/Metadata binding mismatch")
	}
	owner.OwnerNode = w.cfg.Node
	owner.OwnerSandboxID = ""
	owner.State = "ready"
	owner.PinnedArchive = e.ArchiveKey
	owner.Generation++
	_, err = w.writeOwner(ctx, owner, etag)
	return err
}
func (w *worker) adminOwnership(rw http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(rw, "POST required", 405)
		return
	}
	var entries []transferEntry
	if json.NewDecoder(http.MaxBytesReader(rw, r.Body, 8<<20)).Decode(&entries) != nil || len(entries) == 0 || len(entries) > 100 {
		http.Error(rw, "invalid ownership request", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	views := make([]ownershipView, 0, len(entries))
	for _, e := range entries {
		var changeErr error
		if r.URL.Path == "/prepare-import" {
			changeErr = w.prepareLogicalImport(ctx, e)
		}
		if r.URL.Path == "/takeover" {
			changeErr = w.takeover(ctx, e)
		}
		view, err := w.ownershipView(ctx, e)
		if changeErr != nil {
			view.Error = changeErr.Error()
		} else if err != nil {
			view.Error = err.Error()
		}
		views = append(views, view)
	}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(views)
}
