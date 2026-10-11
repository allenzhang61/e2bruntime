package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRestoreRequiresOwnerAndNeverUsesTakeover(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(fmt.Sprint(owned), func(t *testing.T) {
			paths := []string{}
			rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				switch r.URL.Path {
				case "/ownership", "/prepare-import":
					json.NewEncoder(w).Encode([]any{map[string]any{"owned": owned, "owner": map[string]any{"owner_node": "runtime2", "generation": 3}}})
				case "/imports":
					var entries []map[string]any
					if err := json.NewDecoder(r.Body).Decode(&entries); err != nil || len(entries) != 1 || entries[0]["ownership_generation"] != float64(3) {
						t.Error("import did not use prepared owner generation")
					}
					fmt.Fprint(w, `{"status":"completed","results":[{"status":"succeeded","sandbox_id":"restored"}]}`)
				case "/sandboxes/restored":
					fmt.Fprint(w, `{"sandboxID":"restored","state":"running"}`)
				default:
					t.Errorf("unexpected call %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer rt.Close()
			j := job{ID: "op", LogicID: "workspace-abc123", Target: "runtime2", Status: "queued"}
			s := server{cfg: config{Data: t.TempDir(), Bucket: "test"}, http: http.DefaultClient, busy: true, jobs: []job{j}}
			s.run(j, runtimeConfig{Name: "runtime2", API: rt.URL, Admin: rt.URL}, image{Node: "runtime1", Instance: "original", Archive: "archive"})
			if owned {
				if s.jobs[0].Status != "succeeded" || s.jobs[0].Instance != "restored" || len(paths) != 4 {
					t.Fatalf("restore failed: %+v calls=%v", s.jobs, paths)
				}
			} else if s.jobs[0].Status != "failed" || len(paths) != 1 {
				t.Fatalf("unowned backup imported: %+v calls=%v", s.jobs, paths)
			}
			if s.busy {
				t.Fatal("operation lock retained")
			}
		})
	}
}
