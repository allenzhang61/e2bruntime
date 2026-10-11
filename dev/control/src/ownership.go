package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gin-gonic/gin"
)

// switchRuntime only updates S3. It never invokes an import or requires the
// destination Runtime to be reachable.
func (s *server) switchRuntime(c *gin.Context) {
	var req struct {
		LogicID string `json:"logic_id" binding:"required"`
		Target  string `json:"target_runtime" binding:"required"`
	}
	if !bindJSON(c, &req, 4096, "invalid request") {
		return
	}
	valid := false
	for _, rt := range s.cfg.Runtimes {
		if rt.Name == req.Target {
			valid = true
		}
	}
	if !valid {
		apiError(c, http.StatusBadRequest, "unknown Runtime")
		return
	}
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		apiError(c, http.StatusConflict, "another operation is running")
		return
	}
	s.busy = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.busy = false; s.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	v, err := s.inventory(ctx)
	if err != nil {
		apiError(c, http.StatusBadGateway, err.Error())
		return
	}
	for _, row := range v.Logics {
		if row.ID != req.LogicID && row.OriginNode+"/"+row.OriginID != req.LogicID {
			continue
		}
		updated, err := s.assignOwner(ctx, row, req.Target)
		if err != nil {
			apiError(c, http.StatusConflict, err.Error())
			return
		}
		c.JSON(http.StatusOK, gin.H{"logic_id": row.ID, "owner": updated})
		return
	}
	apiError(c, http.StatusNotFound, "Logic ID not found")
}

func (s *server) assignOwner(ctx context.Context, row logic, target string) (owner, error) {
	key := "control/backup-owners/" + row.OriginNode + "/" + row.OriginID + ".json"
	record := map[string]any{}
	etag := ""
	obj, err := s.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key)})
	var old owner
	if err == nil {
		raw, readErr := io.ReadAll(io.LimitReader(obj.Body, 16385))
		obj.Body.Close()
		etag = aws.ToString(obj.ETag)
		if readErr != nil || len(raw) > 16384 || json.Unmarshal(raw, &record) != nil || json.Unmarshal(raw, &old) != nil || etag == "" || record["schema_version"] != float64(1) || record["logical_node"] != row.OriginNode || record["logical_sandbox_id"] != row.OriginID || old.Generation < 1 {
			return old, fmt.Errorf("invalid ownership record; switch denied")
		}
	} else if !s3Code(err, "NoSuchKey", "NotFound", "404") {
		return old, fmt.Errorf("ownership unavailable; switch denied")
	}
	if old.State == "importing" {
		return old, fmt.Errorf("previous import incomplete; inspect target before switching")
	}
	if old.Node == target {
		return old, nil
	}
	next := owner{Node: target, State: "ready", Generation: old.Generation + 1}
	if len(row.Images) > 0 {
		next.Archive = row.Images[0].Archive
	}
	for _, instance := range row.Instances {
		if instance.Runtime == target {
			if next.Instance != "" {
				return old, fmt.Errorf("multiple instances on target Runtime")
			}
			next.Instance = instance.ID
			next.State = "active"
		}
	}
	record["schema_version"] = 1
	record["logical_node"] = row.OriginNode
	record["logical_sandbox_id"] = row.OriginID
	record["owner_node"] = next.Node
	record["owner_sandbox_id"] = next.Instance
	record["state"] = next.State
	record["generation"] = next.Generation
	record["pinned_archive"] = next.Archive
	record["updated_at_utc"] = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return old, err
	}
	input := &s3.PutObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key), Body: bytes.NewReader(raw), ContentType: aws.String("application/json")}
	if etag == "" {
		input.IfNoneMatch = aws.String("*")
	} else {
		input.IfMatch = aws.String(etag)
	}
	if _, err = s.s3.PutObject(ctx, input); err != nil {
		return old, fmt.Errorf("ownership conditional write failed; refresh before retry")
	}
	return next, nil
}
