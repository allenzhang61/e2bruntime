package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	clientgo "github.com/allenzhang61/e2bruntime/dev/control/src/clientgo"
	"github.com/gin-gonic/gin"
)

type debugAction struct {
	ID      string         `json:"id"`
	Label   string         `json:"label"`
	Example map[string]any `json:"example"`
}

var debugActions = []debugAction{
	{"list", "实例列表（运行中与休眠）", map[string]any{}},
	{"create", "创建实例", map[string]any{"template_id": "base", "timeout": 300}},
	{"get", "查询实例", map[string]any{"sandbox_id": "填写 Instance ID"}},
	{"delete", "删除", map[string]any{"sandbox_id": "填写 Instance ID"}},
	{"command", "执行命令", map[string]any{"sandbox_id": "填写 Instance ID", "command": "uname -a; nproc; free -m"}},
	{"write-file", "写文件", map[string]any{"sandbox_id": "填写 Instance ID", "path": "/home/user/debug.txt", "content": "Hello from Control"}},
	{"read-file", "读文件", map[string]any{"sandbox_id": "填写 Instance ID", "path": "/home/user/debug.txt"}},
	{"filesystem-snapshot", "文件系统快照（源实例继续运行）", map[string]any{"sandbox_id": "填写 Instance ID"}},
	{"delete-snapshot", "删除快照模板", map[string]any{"snapshot_id": "填写 Snapshot ID"}},
	{"hibernate", "休眠", map[string]any{"sandbox_id": "填写 Instance ID"}},
	{"wake", "唤醒", map[string]any{"sandbox_id": "填写 Instance ID", "timeout": 300}},
	{"timeout", "设置运行时限（秒）", map[string]any{"sandbox_id": "填写 Instance ID", "timeout": 300}},
}
var debugIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type debugRequest struct {
	Runtime    string `json:"runtime" binding:"required"`
	SandboxID  string `json:"sandbox_id"`
	SnapshotID string `json:"snapshot_id"`
	TemplateID string `json:"template_id" binding:"max=128"`
	NextToken  string `json:"next_token"`
	Timeout    int    `json:"timeout" binding:"omitempty,min=1,max=3600"`
	Path       string `json:"path" binding:"max=4096"`
	Content    string `json:"content"`
	Command    string `json:"command" binding:"max=16384"`
}

// Only named operations against configured Runtimes are accepted; callers cannot supply URLs or credentials.
func (s *server) debugOperation(c *gin.Context) {
	start := time.Now()
	action := c.Param("action")
	found := false
	for _, candidate := range debugActions {
		if candidate.ID == action {
			found = true
			break
		}
	}
	if !found {
		apiError(c, http.StatusNotFound, "unknown debug operation")
		return
	}
	var req debugRequest
	if !bindJSON(c, &req, 128<<10, "invalid debug request (maximum 128 KiB)") {
		return
	}
	var rt runtimeConfig
	for _, candidate := range s.cfg.Runtimes {
		if candidate.Name == req.Runtime {
			rt = candidate
			break
		}
	}
	if rt.Name == "" {
		apiError(c, http.StatusBadRequest, "unknown Runtime")
		return
	}
	if action != "list" && action != "create" && action != "delete-snapshot" && !debugIDPattern.MatchString(req.SandboxID) {
		apiError(c, http.StatusBadRequest, "valid sandbox_id required")
		return
	}
	if req.Timeout == 0 {
		req.Timeout = 300
	}
	if req.TemplateID == "" {
		req.TemplateID = "base"
	}
	if (action == "read-file" || action == "write-file") && (!strings.HasPrefix(req.Path, "/")) {
		apiError(c, http.StatusBadRequest, "absolute file path required")
		return
	}
	if action == "command" && req.Command == "" {
		apiError(c, http.StatusBadRequest, "command required (maximum 16 KiB)")
		return
	}
	template, _, _ := strings.Cut(req.SnapshotID, ":")
	if action == "delete-snapshot" && !debugIDPattern.MatchString(template) {
		apiError(c, http.StatusBadRequest, "valid snapshot_id required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	client := clientgo.NewClient(rt.API, rt.Proxy, rt.Key)
	client.HTTP.Timeout = 40 * time.Second
	var result any
	var err error
	var token string
	path := "/sandboxes/" + url.PathEscape(req.SandboxID)
	switch action {
	case "list":
		// Return the Runtime response and pagination token, without silently dropping further pages.
		var data any
		var next string
		listPath := "/v2/sandboxes?state=running,paused&limit=100"
		if req.NextToken != "" {
			listPath += "&nextToken=" + url.QueryEscape(req.NextToken)
		}
		next, err = s.call(ctx, rt, false, "GET", listPath, nil, &data)
		result = gin.H{"instances": data, "next_token": next}
	case "create":
		errCall := map[string]any{"templateID": req.TemplateID, "timeout": req.Timeout, "metadata": map[string]string{"purpose": "control-debug"}}
		_, err = s.call(ctx, rt, false, "POST", "/v2/sandboxes", errCall, &result)
	case "get":
		_, err = s.call(ctx, rt, false, "GET", path, nil, &result)
	case "delete":
		err = client.Delete(ctx, req.SandboxID)
		result = gin.H{"deleted": err == nil, "sandbox_id": req.SandboxID}
	case "filesystem-snapshot":
		var id string
		id, err = client.FilesystemSnapshot(ctx, req.SandboxID)
		result = gin.H{"snapshot_id": id, "memory": false}
	case "delete-snapshot":
		err = client.DeleteSnapshot(ctx, req.SnapshotID)
		result = gin.H{"deleted": err == nil, "snapshot_id": req.SnapshotID}
	case "hibernate":
		_, err = s.call(ctx, rt, false, "POST", path+"/pause", gin.H{"memory": true}, nil)
		result = gin.H{"sandbox_id": req.SandboxID, "hibernate_requested": err == nil, "s3_backup_requested": false}
	case "wake":
		_, err = s.call(ctx, rt, false, "POST", "/v2"+path+"/connect", gin.H{"timeout": req.Timeout}, &result)
	case "timeout":
		_, err = s.call(ctx, rt, false, "POST", path+"/timeout", gin.H{"timeout": req.Timeout}, nil)
		result = gin.H{"sandbox_id": req.SandboxID, "timeout": req.Timeout}
	case "command", "read-file", "write-file":
		var sb clientgo.Sandbox
		sb, _, err = client.Get(ctx, req.SandboxID)
		token = sb.AccessToken
		if err == nil {
			if action == "command" {
				result, err = client.RunCommand(ctx, sb, req.Command)
			}
			if action == "write-file" {
				err = client.WriteFile(ctx, sb, req.Path, []byte(req.Content))
				result = gin.H{"path": req.Path, "bytes": len([]byte(req.Content)), "written": err == nil}
			}
			if action == "read-file" {
				var data []byte
				data, err = client.ReadFile(ctx, sb, req.Path)
				result = gin.H{"path": req.Path, "bytes": len(data), "content": string(data)}
			}
		}
	}
	response := gin.H{"runtime": rt.Name, "action": action, "elapsed_ms": float64(time.Since(start).Microseconds()) / 1000, "success": err == nil, "result": result}
	status := http.StatusOK
	if err != nil {
		status = http.StatusBadGateway
		message, _, _ := strings.Cut(err.Error(), ": ")
		response["error"] = message
	}
	// Runtime responses may contain envd tokens; do not expose deployment/guest credentials to the UI.
	clean := s.redactDebug(response, token)
	c.JSON(status, clean)
}

func (s *server) redactDebug(value any, token string) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return gin.H{"success": false, "error": "response serialization failed"}
	}
	var decoded any
	_ = json.Unmarshal(raw, &decoded)
	var clean func(any) any
	clean = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				lower := strings.ToLower(k)
				if strings.Contains(lower, "token") && lower != "next_token" || strings.Contains(lower, "secret") || strings.Contains(lower, "apikey") || strings.Contains(lower, "api_key") {
					x[k] = "[REDACTED]"
				} else {
					x[k] = clean(v)
				}
			}
			return x
		case []any:
			for i := range x {
				x[i] = clean(x[i])
			}
			return x
		case string:
			secrets := []string{s.cfg.AK, s.cfg.SK, token}
			for _, rt := range s.cfg.Runtimes {
				secrets = append(secrets, rt.Key)
			}
			for _, secret := range secrets {
				if secret != "" {
					x = strings.ReplaceAll(x, secret, "[REDACTED]")
				}
			}
			return x
		default:
			return v
		}
	}
	return clean(decoded)
}
