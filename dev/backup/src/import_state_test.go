package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestImportResultCurrentState(t *testing.T) {
	for _, tc := range []struct {
		name       string
		code       int
		body, want string
	}{
		{"running", 200, `{"sandboxID":"instance1","state":"running"}`, "running"},
		{"paused", 200, `{"sandboxID":"instance1","state":"paused"}`, "paused"},
		{"deleted", 404, `{}`, "missing"},
		{"auth", 403, `{}`, "unknown"},
		{"unavailable", 503, `{}`, "unknown"},
		{"wrong-instance", 200, `{"sandboxID":"other","state":"running"}`, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/sandboxes/instance1" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			manager := newImportManager(&worker{api: newAPI(config{API: server.URL})})
			job := importJob{Status: "completed", Results: []importResult{{Status: "succeeded", SandboxID: "instance1"}}}
			manager.refreshCurrentState(context.Background(), &job)
			if job.Results[0].CurrentState != tc.want || job.Results[0].Status != "succeeded" {
				t.Fatalf("bad live/historical state: %+v", job)
			}
		})
	}
}
