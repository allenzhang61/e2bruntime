package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestDebugLive(t *testing.T) {
	if os.Getenv("CONTROL_DEBUG_LIVE") != "1" {
		t.Skip("set CONTROL_DEBUG_LIVE=1 against the development Control service")
	}
	base := os.Getenv("CONTROL_DEBUG_URL") // Example: http://localhost:13021
	if base == "" {
		base = "http://localhost:13021"
	}
	client := &http.Client{Timeout: 55 * time.Second}
	for _, node := range []string{"runtime1", "runtime2"} {
		t.Run(node, func(t *testing.T) {
			call := func(action string, params map[string]any) map[string]any {
				t.Helper()
				params["runtime"] = node
				raw, _ := json.Marshal(params)
				req, _ := http.NewRequest("POST", base+"/api/debug/"+action, bytes.NewReader(raw))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Control-Request", "1")
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				var result struct {
					Success bool           `json:"success"`
					Result  map[string]any `json:"result"`
					Elapsed float64        `json:"elapsed_ms"`
					Error   string         `json:"error"`
				}
				if json.NewDecoder(resp.Body).Decode(&result) != nil || resp.StatusCode != 200 || !result.Success {
					t.Fatalf("%s HTTP %d: %s", action, resp.StatusCode, result.Error)
				}
				t.Logf("%s %.2f ms", action, result.Elapsed)
				return result.Result
			}
			created := call("create", map[string]any{"timeout": 120})
			id, _ := created["sandboxID"].(string)
			if id == "" {
				t.Fatal("no instance ID")
			}
			// This cleanup deletes only the sandbox created by this test.
			t.Cleanup(func() { call("delete", map[string]any{"sandbox_id": id}) })
			call("write-file", map[string]any{"sandbox_id": id, "path": "/home/user/control-debug.txt", "content": "Control Debug verification"})
			read := call("read-file", map[string]any{"sandbox_id": id, "path": "/home/user/control-debug.txt"})
			if read["content"] != "Control Debug verification" {
				t.Fatal("file contents mismatch")
			}
			command := call("command", map[string]any{"sandbox_id": id, "command": "cat /home/user/control-debug.txt; uname -s"})
			if command["ExitCode"] != float64(0) {
				t.Fatal("command failed")
			}
			snapshot := call("filesystem-snapshot", map[string]any{"sandbox_id": id})
			snapshotID, _ := snapshot["snapshot_id"].(string)
			if snapshotID == "" {
				t.Fatal("snapshot ID missing")
			}
			call("delete-snapshot", map[string]any{"snapshot_id": snapshotID})
			call("hibernate", map[string]any{"sandbox_id": id})
			paused := call("get", map[string]any{"sandbox_id": id})
			if paused["state"] != "paused" {
				t.Fatal("hibernate did not pause instance")
			}
			call("wake", map[string]any{"sandbox_id": id, "timeout": 120})
			read = call("read-file", map[string]any{"sandbox_id": id, "path": "/home/user/control-debug.txt"})
			if read["content"] != "Control Debug verification" {
				t.Fatal("wake lost file")
			}
		})
	}
}
