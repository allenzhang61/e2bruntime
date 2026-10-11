package runtimetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFilesystemSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("live filesystem snapshot test")
	}
	cfg := configInit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	nodes := targets(t, ctx, cfg)
	t.Log("result directory:", cfg.OutputDir)
	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		t.Run(node.Name, func(t *testing.T) {
			c := NewClient(node.API, node.Proxy, node.Key)
			attach := func(sb Sandbox, err error) Sandbox {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				if sb.ID == "" {
					t.Fatal("sandbox ID missing")
				}
				id := sb.ID
				t.Cleanup(func() {
					clean, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if err := c.Delete(clean, id); err != nil {
						t.Error("cleanup:", err)
					}
				})
				if sb.AccessToken == "" {
					var err error
					sb, _, err = c.Get(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					sb.ID = id
				}
				return sb
			}
			run := func(sb Sandbox, cmd string) string {
				t.Helper()
				result, err := c.RunCommand(ctx, sb, cmd)
				if err != nil {
					t.Fatal("command:", err)
				}
				return strings.TrimSpace(result.Stdout)
			}
			source := attach(c.Create(ctx))
			nonce := make([]byte, 12)
			if _, err := rand.Read(nonce); err != nil {
				t.Fatal(err)
			}
			marker := hex.EncodeToString(nonce)
			file := "/home/user/fs-snapshot.txt"
			expected := []byte("filesystem snapshot marker=" + marker + "\n")
			if err := c.WriteFile(ctx, source, file, expected); err != nil {
				t.Fatal(err)
			}
			boot := run(source, "cat /proc/sys/kernel/random/boot_id")
			process := "fs-e2e-" + marker
			run(source, fmt.Sprintf("(exec -a %s sleep 120) >/dev/null 2>&1 &", process))
			processState := fmt.Sprintf("if pgrep -f '^%s ' >/dev/null; then echo running; else echo absent; fi", process)
			if run(source, processState) != "running" {
				t.Fatal("source process did not start")
			}
			start := time.Now()
			snapshot, err := c.FilesystemSnapshot(ctx, source.ID)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatal("filesystem snapshot:", err)
			}
			// Registered before clone cleanup: t.Cleanup runs in reverse order.
			t.Cleanup(func() {
				clean, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := c.DeleteSnapshot(clean, snapshot); err != nil {
					t.Error("snapshot cleanup:", err)
				}
			})
			if run(source, "cat /proc/sys/kernel/random/boot_id") != boot || run(source, processState) != "running" {
				t.Fatal("snapshot interrupted the source VM/process lifetime")
			}
			if err := c.WriteFile(ctx, source, file, []byte("changed after snapshot\n")); err != nil {
				t.Fatal(err)
			}
			start = time.Now()
			restored := attach(c.CreateFromTemplate(ctx, snapshot))
			restoreElapsed := time.Since(start)
			actual, err := c.ReadFile(ctx, restored, file)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual, expected) {
				t.Fatal("snapshot did not restore captured file contents")
			}
			if run(restored, "cat /proc/sys/kernel/random/boot_id") == boot {
				t.Fatal("filesystem snapshot restored old boot/memory state")
			}
			if run(restored, processState) != "absent" {
				t.Fatal("filesystem snapshot restored an old process")
			}
			data, _ := json.MarshalIndent(map[string]any{"node": node.Name, "snapshot_id": snapshot, "snapshot_ms": float64(elapsed) / float64(time.Millisecond), "restore_ms": float64(restoreElapsed) / float64(time.Millisecond), "file_verified": true, "source_kept_running": true, "restored_cold_boot": true, "old_process_absent": true}, "", "  ")
			if err := os.WriteFile(filepath.Join(cfg.OutputDir, node.Name+"-filesystem.json"), append(data, '\n'), 0644); err != nil {
				t.Fatal(err)
			}
			t.Logf("snapshot=%s duration=%s restore=%s: files preserved; source kept running; restored cold-booted without old process", snapshot, elapsed, restoreElapsed)
		})
	}
}
