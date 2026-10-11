package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExtendInstanceAddsToDeadlineAndPreservesLifecycle(t *testing.T) {
	for _, state := range []string{"running", "paused"} {
		t.Run(state, func(t *testing.T) {
			deadline := time.Now().Add(20 * time.Minute)
			calls := 0
			rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/sandboxes/instance/timeout" {
					calls++
					var req struct {
						Timeout int `json:"timeout"`
					}
					json.NewDecoder(r.Body).Decode(&req)
					if req.Timeout < 1799 || req.Timeout > 1801 {
						t.Errorf("deadline shortened or delay not added: %d", req.Timeout)
					}
					deadline = time.Now().Add(time.Duration(req.Timeout) * time.Second)
					w.WriteHeader(204)
					return
				}
				fmt.Fprintf(w, `{"sandboxID":"instance","state":%q,"endAt":%q,"lifecycle":{"onTimeout":"pause"}}`, state, deadline.Format(time.RFC3339Nano))
			}))
			defer rt.Close()
			s := server{cfg: config{Runtimes: []runtimeConfig{{Name: "runtime1", API: rt.URL}}}, http: http.DefaultClient}
			r := httptest.NewRequest("POST", "http://localhost:13021/api/instances/extend", strings.NewReader(`{"runtime":"runtime1","sandbox_id":"instance","delay_seconds":600}`))
			r.Header.Set("X-Control-Request", "1")
			out := httptest.NewRecorder()
			s.handler().ServeHTTP(out, r)
			if state == "running" {
				if out.Code != 200 || calls != 1 || !strings.Contains(out.Body.String(), `"on_timeout":"pause"`) {
					t.Fatalf("extension failed: %d %s", out.Code, out.Body.String())
				}
			} else if out.Code != 409 || calls != 0 {
				t.Fatal("paused instance was extended or woken")
			}
		})
	}
}
