package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogicalVacancyIncludesPausedAndFailsClosed(t *testing.T) {
	for _, state := range []string{"running", "paused", "unavailable", "empty"} {
		t.Run(state, func(t *testing.T) {
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if state == "unavailable" {
					w.WriteHeader(503)
					return
				}
				if r.URL.Query().Get("state") == state {
					w.Write([]byte(`[{"sandboxID":"newinstance","metadata":{"dev_backup_origin_node":"runtime1","dev_backup_origin_id":"original"}}]`))
				} else {
					w.Write([]byte(`[]`))
				}
			}))
			defer mock.Close()
			w := worker{cfg: config{Node: "runtime2"}, api: newAPI(config{API: mock.URL})}
			err := w.requireLogicalVacancy(context.Background(), "runtime1", "original")
			if (err == nil) != (state == "empty") {
				t.Fatalf("state=%s error=%v", state, err)
			}
		})
	}
}
