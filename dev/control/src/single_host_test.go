package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSingleHostConfiguration(t *testing.T) {
	for _, key := range []string{"RUSTFS_ENDPOINT", "RUSTFS_ACCESS_KEY", "RUSTFS_SECRET_KEY", "RUSTFS_BUCKET_NAME", "RUNTIME1_API_KEY"} {
		t.Setenv(key, "test-value")
	}
	t.Setenv("CONTROL_RUNTIMES", "runtime1")
	t.Setenv("RUNTIME2_API_KEY", "")
	t.Setenv("CONTROL_ALLOWED_HOSTS", "192.0.2.10:13021")
	cfg, err := configInit()
	if err != nil || len(cfg.Runtimes) != 1 || cfg.Runtimes[0].Name != "runtime1" {
		t.Fatalf("unexpected single-node configuration: %v", err)
	}
	if cfg.Listen != value("CONTROL_LISTEN", "127.0.0.1:13021") {
		t.Fatal("default listen policy changed")
	}
	router := (&server{cfg: cfg, jobs: []job{}}).handler()
	for _, tc := range []struct {
		method, path, host, origin string
		status                     int
	}{
		{"GET", "/?view=runtime1", "localhost:13021", "", 200},
		{"GET", "/?view=runtime2", "localhost:13021", "", 400},
		{"GET", "/debug", "localhost:13021", "", 200},
		{"GET", "/health", "192.0.2.10:13021", "", 200},
		{"GET", "/health", "192.0.2.10:13022", "", 403},
		{"GET", "/health", "untrusted.example:13021", "", 403},
		{"POST", "/api/switch", "192.0.2.10:13021", "http://192.0.2.10:13021", 400},
		{"POST", "/api/switch", "192.0.2.10:13021", "http://foreign.example", 403},
		{"POST", "/api/switch", "192.0.2.10:13021", "https://192.0.2.10:13021", 403},
	} {
		req := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, strings.NewReader("{}"))
		req.Header.Set("Origin", tc.origin)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != tc.status {
			t.Fatalf("%s %s: got %d, want %d", tc.host, tc.path, out.Code, tc.status)
		}
		if tc.status == 200 && strings.Contains(out.Body.String(), "runtime2") {
			t.Fatal("disabled Runtime leaked into navigation or Debug selection")
		}
	}
	for _, names := range []string{"runtime1,runtime1", "other", "runtime1,"} {
		t.Setenv("CONTROL_RUNTIMES", names)
		if _, err := configInit(); err == nil {
			t.Fatalf("invalid Runtime list accepted: %q", names)
		}
	}
}
