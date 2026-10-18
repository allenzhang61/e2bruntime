package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestHTTPRoutingAndAccess(t *testing.T) {
	s := server{jobs: []job{}, cfg: config{Runtimes: []runtimeConfig{{Name: "runtime1"}, {Name: "runtime2"}}}}
	router := s.handler()
	for _, tc := range []struct {
		name, method, path, host, origin, cli, body, wantBody string
		status                                                int
	}{
		{name: "health", method: "GET", path: "/health", wantBody: `"status":"ok"`, status: 200},
		{name: "page", method: "GET", path: "/", wantBody: "<html", status: 200},
		{name: "runtime view", method: "GET", path: "/?view=runtime2", wantBody: `data-view="runtime2"`, status: 200},
		{name: "unknown runtime view", method: "GET", path: "/?view=other", wantBody: "unknown Runtime view", status: 400},
		{name: "debug tab", method: "GET", path: "/debug", wantBody: `href="/debug" aria-current="page"`, status: 200},
		{name: "backup tab", method: "GET", path: "/backups", wantBody: `href="/backups" aria-current="page"`, status: 200},
		{name: "empty jobs", method: "GET", path: "/api/jobs", wantBody: "[]", status: 200},
		{name: "foreign host", method: "GET", path: "/health", host: "remote.example", status: 403},
		{name: "missing origin and CLI header", method: "POST", path: "/api/switch", body: "{}", status: 403},
		{name: "CLI header", method: "POST", path: "/api/switch", cli: "1", body: `{"logic_id":"a","target_runtime":"missing"}`, wantBody: "unknown Runtime", status: 400},
		{name: "same origin", method: "POST", path: "/api/switch", origin: "http://localhost:13021", body: `{"logic_id":"a","target_runtime":"missing"}`, wantBody: "unknown Runtime", status: 400},
		{name: "foreign origin even with CLI header", method: "POST", path: "/api/switch", origin: "http://foreign.example", cli: "1", body: "{}", status: 403},
		{name: "invalid JSON", method: "POST", path: "/api/switch", cli: "1", body: "{", wantBody: "invalid request", status: 400},
		{name: "body limit", method: "POST", path: "/api/switch", cli: "1", body: `{"logic_id":"` + strings.Repeat("x", 4096) + `"}`, wantBody: "invalid request", status: 400},
		{name: "unknown route", method: "GET", path: "/missing", status: 404},
		{name: "wrong method", method: "DELETE", path: "/api/jobs", status: 404},
		{name: "no implicit redirect", method: "GET", path: "/api/jobs/", status: 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://localhost:13021"+tc.path, strings.NewReader(tc.body))
			if tc.host != "" {
				req.Host = tc.host
			}
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-Control-Request", tc.cli)
			out := httptest.NewRecorder()
			router.ServeHTTP(out, req)
			if out.Code != tc.status || !strings.Contains(out.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s", out.Code, out.Body.String())
			}
			if out.Header().Get("Cache-Control") != "no-store" || out.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatal("response security headers missing")
			}
			if s.busy {
				t.Fatal("rejected request left the switch lock held")
			}
		})
	}
}

func TestHTTPRecovery(t *testing.T) {
	s := server{}
	router := s.handler()
	router.GET("/panic", func(c *gin.Context) { panic("private request state") })
	out := httptest.NewRecorder()
	router.ServeHTTP(out, httptest.NewRequest(http.MethodGet, "http://localhost:13021/panic", nil))
	if out.Code != 500 || strings.Contains(out.Body.String(), "private request state") {
		t.Fatalf("unsafe panic response: %d %s", out.Code, out.Body.String())
	}
}
