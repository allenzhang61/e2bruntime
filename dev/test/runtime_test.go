package runtimetest

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type target struct{ Name, API, Proxy, Key string }
type report struct {
	Node         string             `json:"node"`
	SandboxID    string             `json:"sandbox_id"`
	File         string             `json:"sandbox_file"`
	Bytes        int                `json:"bytes"`
	SHA256       string             `json:"sha256"`
	FileVerified bool               `json:"file_verified"`
	Deleted      bool               `json:"deleted"`
	Timings      map[string]float64 `json:"timings_ms"`
}

type testConfig struct {
	SSHHost   string
	RemoteDir string
	OutputDir string
	Manual    bool
	Nodes     []target
}

// configInit is the only place this test reads environment variables.
// Empty values use the defaults below. Example keys are placeholders, never real credentials.
func configInit(t *testing.T) testConfig {
	t.Helper()
	env := func(name, fallback string) string {
		if value := os.Getenv(name); value != "" {
			return value
		}
		return fallback
	}
	cfg := testConfig{
		// SSH target; example: zyl@192.168.1.211
		SSHHost: env("RUNTIME_SSH_HOST", "zyl@192.168.1.211"),
		// Existing Vagrant state directory on the SSH host; example/default below.
		RemoteDir: env("RUNTIME_REMOTE_DIR", "/home/zyl/e2bruntime/deploy/two-vm"),
		// Local result directory; example: out/manual-run. Default: a unique UTC run directory.
		OutputDir: env("RUNTIME_TEST_OUT", filepath.Join("out", time.Now().UTC().Format("20060102T150405.000000000Z"))),
		Nodes: []target{
			{
				Name:  "runtime1",
				API:   env("RUNTIME1_API_URL", ""),     // Example: http://127.0.0.1:13000
				Proxy: env("RUNTIME1_SANDBOX_URL", ""), // Example: http://127.0.0.1:13002
				Key:   env("RUNTIME1_API_KEY", ""),     // Example: e2b_<runtime1-team-key>
			},
			{
				Name:  "runtime2",
				API:   env("RUNTIME2_API_URL", ""),     // Example: http://127.0.0.1:23000
				Proxy: env("RUNTIME2_SANDBOX_URL", ""), // Example: http://127.0.0.1:23002
				Key:   env("RUNTIME2_API_KEY", ""),     // Example: e2b_<runtime2-team-key>
			},
		},
	}
	// No API URL: create a temporary SSH tunnel and fetch missing keys from each VM.
	// Either API URL set: direct mode requires both complete URL/proxy/key groups.
	cfg.Manual = cfg.Nodes[0].API != "" || cfg.Nodes[1].API != ""
	if cfg.Manual {
		for i, node := range cfg.Nodes {
			if node.API == "" || node.Proxy == "" || node.Key == "" {
				t.Fatalf("direct mode requires RUNTIME%d_API_URL, RUNTIME%d_SANDBOX_URL and RUNTIME%d_API_KEY", i+1, i+1, i+1)
			}
		}
	} else {
		if !regexp.MustCompile(`^[a-zA-Z0-9_.@:-]+$`).MatchString(cfg.SSHHost) || strings.HasPrefix(cfg.SSHHost, "-") {
			t.Fatal("invalid RUNTIME_SSH_HOST")
		}
		if !regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`).MatchString(cfg.RemoteDir) {
			t.Fatal("invalid RUNTIME_REMOTE_DIR")
		}
	}
	return cfg
}

// With no explicit API URLs, open a temporary tunnel to the existing Vagrant guests.
func targets(t *testing.T, ctx context.Context, cfg testConfig) []target {
	t.Helper()
	list := append([]target(nil), cfg.Nodes...)
	if cfg.Manual {
		return list
	}
	args := []string{"-N", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15"}
	for i := range list {
		for j := 0; j < 2; j++ {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			port := listener.Addr().(*net.TCPAddr).Port
			listener.Close()
			remotePort := (i+1)*10000 + 3000 + j*2
			args = append(args, "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, remotePort))
			address := fmt.Sprintf("http://127.0.0.1:%d", port)
			if j == 0 {
				list[i].API = address
			} else {
				list[i].Proxy = address
			}
		}
	}
	args = append(args, cfg.SSHHost)
	tunnel := exec.CommandContext(ctx, "ssh", args...)
	if err := tunnel.Start(); err != nil {
		t.Fatal("start SSH tunnel:", err)
	}
	done := make(chan error, 1)
	go func() { done <- tunnel.Wait() }()
	t.Cleanup(func() {
		_ = tunnel.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("SSH tunnel did not exit")
		}
	})
	probe := &http.Client{Timeout: time.Second}
	for _, node := range list {
		deadline := time.Now().Add(20 * time.Second)
		for {
			select {
			case err := <-done:
				done <- err
				t.Fatalf("SSH tunnel exited: %v", err)
			default:
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, node.API+"/health", nil)
			if err != nil {
				t.Fatal(err)
			}
			res, err := probe.Do(req)
			if err == nil {
				res.Body.Close()
				if res.StatusCode == http.StatusOK {
					break
				}
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				t.Fatal("Runtime API unavailable:", node.Name)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	for i := range list {
		if list[i].Key != "" {
			continue
		}
		command := fmt.Sprintf("cd '%s' && vagrant ssh %s -c 'sudo cat /opt/e2b-runtime/sdk.env'", cfg.RemoteDir, list[i].Name)
		raw, err := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", cfg.SSHHost, command).Output()
		if err != nil {
			t.Fatalf("read %s credentials via SSH: %v", list[i].Name, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "export E2B_API_KEY="); ok {
				list[i].Key = strings.Trim(value, "\r\"'")
			}
		}
		if list[i].Key == "" {
			t.Fatal("missing API key for", list[i].Name)
		}
	}
	return list
}

func TestRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("live Runtime integration test")
	}
	cfg := configInit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	nodes := targets(t, ctx, cfg)
	out := cfg.OutputDir
	if err := os.MkdirAll(out, 0755); err != nil {
		t.Fatal(err)
	}
	t.Log("result directory:", out)
	for _, node := range nodes {
		t.Run(node.Name, func(t *testing.T) {
			c := NewClient(node.API, node.Proxy, node.Key)
			r := report{Node: node.Name, File: "/home/user/e2e.txt", Timings: map[string]float64{}}
			start := time.Now()
			sb, err := c.Create(ctx)
			r.Timings["create"] = float64(time.Since(start)) / float64(time.Millisecond)
			if err != nil {
				t.Fatal("create:", err)
			}
			if sb.ID == "" {
				t.Fatal("create response has no sandbox ID")
			}
			r.SandboxID = sb.ID
			deleted := false
			// Independent cleanup budget even if the main test context expires.
			t.Cleanup(func() {
				if !deleted {
					clean, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if err := c.Delete(clean, r.SandboxID); err != nil {
						t.Error("cleanup sandbox:", err)
					}
				}
			})
			if sb.AccessToken == "" {
				sb, _, err = c.Get(ctx, r.SandboxID)
				if err != nil {
					t.Fatal("connection details:", err)
				}
				sb.ID = r.SandboxID
			}
			if sb.AccessToken == "" {
				t.Fatal("missing envd access token")
			}
			nonce := make([]byte, 16)
			if _, err = rand.Read(nonce); err != nil {
				t.Fatal(err)
			}
			expected := []byte(fmt.Sprintf("Hello from %s!\nnonce=%s\n", node.Name, hex.EncodeToString(nonce)))
			start = time.Now()
			if err = c.WriteFile(ctx, sb, r.File, expected); err != nil {
				t.Fatal("write sandbox file:", err)
			}
			r.Timings["write_file"] = float64(time.Since(start)) / float64(time.Millisecond)
			start = time.Now()
			actual, err := c.ReadFile(ctx, sb, r.File)
			if err != nil {
				t.Fatal("read sandbox file:", err)
			}
			if !bytes.Equal(actual, expected) {
				t.Fatal("sandbox file contents differ from written bytes")
			}
			r.Timings["read_verify"] = float64(time.Since(start)) / float64(time.Millisecond)
			r.Bytes = len(actual)
			sum := sha256.Sum256(actual)
			r.SHA256 = hex.EncodeToString(sum[:])
			r.FileVerified = true
			local := filepath.Join(out, node.Name+".txt")
			if err = os.WriteFile(local, actual, 0644); err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(local)
			if err != nil || !bytes.Equal(saved, expected) {
				t.Fatal("local output file verification failed")
			}
			start = time.Now()
			if err = c.Delete(ctx, sb.ID); err != nil {
				t.Fatal("delete:", err)
			}
			deleted = true
			r.Timings["delete"] = float64(time.Since(start)) / float64(time.Millisecond)
			start = time.Now()
			deadline := time.Now().Add(15 * time.Second)
			for {
				_, status, err := c.Get(ctx, sb.ID)
				if status == http.StatusNotFound {
					break
				}
				if err != nil {
					t.Fatal("verify deletion:", err)
				}
				if time.Now().After(deadline) || ctx.Err() != nil {
					t.Fatal("sandbox still exists after delete")
				}
				time.Sleep(100 * time.Millisecond)
			}
			r.Timings["delete_verify"] = float64(time.Since(start)) / float64(time.Millisecond)
			r.Deleted = true
			data, err := json.MarshalIndent(r, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(out, node.Name+".json"), append(data, '\n'), 0644); err != nil {
				t.Fatal(err)
			}
			t.Logf("sandbox=%s bytes=%d SHA256=%s timings(ms)=%v; file verified, sandbox deleted", sb.ID, r.Bytes, r.SHA256, r.Timings)
		})
	}
}
