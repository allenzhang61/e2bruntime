package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDebugUsesSelectedRuntimeAndKeepsCredentialsPrivate(t *testing.T) {
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-API-Key") != "private-key" {
			t.Error("Runtime API key missing")
		}
		switch r.URL.Path {
		case "/v2/sandboxes":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["timeout"] != float64(300) || body["templateID"] != "base" {
				t.Errorf("bad creation parameters: %+v", body)
			}
		case "/sandboxes/abc/snapshots":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["memory"] != false {
				t.Error("filesystem snapshot enabled memory")
			}
			io.WriteString(w, `{"snapshotID":"snap:default"}`)
			return
		}
		io.WriteString(w, `{"sandboxID":"abc","envdAccessToken":"guest-secret","apiKey":"private-key"}`)
	}))
	defer api.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Access-Token") != "guest-secret" || r.Header.Get("E2b-Sandbox-Id") != "abc" || r.URL.Query().Get("path") != "/home/user/debug.txt" {
			t.Error("guest request credentials/path incorrect")
		}
		io.WriteString(w, "Hello guest-secret private-key")
	}))
	defer proxy.Close()
	s := server{cfg: config{Runtimes: []runtimeConfig{{Name: "runtime2", API: api.URL, Proxy: proxy.URL, Key: "private-key"}}}, http: http.DefaultClient}
	router := s.handler()
	for _, operation := range []struct{ action, body string }{
		{"create", `{"runtime":"runtime2"}`},
		{"filesystem-snapshot", `{"runtime":"runtime2","sandbox_id":"abc"}`},
		{"read-file", `{"runtime":"runtime2","sandbox_id":"abc","path":"/home/user/debug.txt"}`},
	} {
		req := httptest.NewRequest("POST", "http://localhost:13021/api/debug/"+operation.action, strings.NewReader(operation.body))
		req.Header.Set("X-Control-Request", "1")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != 200 || strings.Contains(out.Body.String(), "guest-secret") || strings.Contains(out.Body.String(), "private-key") {
			t.Fatalf("unexpected/private response: %d %s", out.Code, out.Body.String())
		}
		var result map[string]any
		if json.Unmarshal(out.Body.Bytes(), &result) != nil || result["runtime"] != "runtime2" || result["elapsed_ms"] == nil {
			t.Fatal("missing runtime/timing")
		}
	}
	if calls != 3 {
		t.Fatalf("Runtime API calls=%d", calls)
	}
}

func TestDebugRejectsArbitraryDestinationsAndForeignOrigin(t *testing.T) {
	s := server{cfg: config{Runtimes: []runtimeConfig{{Name: "runtime1"}}}}
	router := s.handler()
	for _, tc := range []struct {
		action, body, origin string
		status               int
	}{
		{"create", `{"runtime":"runtime3"}`, "http://localhost:13021", 400},
		{"create", `{"runtime":"runtime1","api_url":"http://foreign.example"}`, "http://localhost:13021", 400},
		{"delete", `{"runtime":"runtime1","sandbox_id":"../other"}`, "http://localhost:13021", 400},
		{"create", `{"runtime":"runtime1"}`, "http://foreign.example", 403},
		{"arbitrary", `{}`, "http://localhost:13021", 404},
	} {
		req := httptest.NewRequest("POST", "http://localhost:13021/api/debug/"+tc.action, strings.NewReader(tc.body))
		req.Header.Set("Origin", tc.origin)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != tc.status {
			t.Fatalf("%s: got %d %s", tc.action, out.Code, out.Body.String())
		}
	}
}
