package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIndependentSchedulesCancelAndExecute(t *testing.T) {
	state := "running"
	nativeDelete := time.Now().UTC().Add(20 * time.Minute)
	pauses, deletes := 0, 0
	rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE":
			deletes++
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/pause"):
			pauses++
			state = "paused"
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/timeout"):
			w.WriteHeader(204)
		default:
			fmt.Fprintf(w, `{"sandboxID":"instance","state":%q,"endAt":%q,"lifecycle":{"onTimeout":"kill"}}`, state, nativeDelete.Format(time.RFC3339Nano))
		}
	}))
	defer rt.Close()
	s := server{cfg: config{Data: t.TempDir(), Runtimes: []runtimeConfig{{Name: "runtime1", API: rt.URL}}}, http: http.DefaultClient}
	send := func(action string, at *time.Time) {
		body, _ := json.Marshal(map[string]any{"runtime": "runtime1", "sandbox_id": "instance", "action": action, "at": at})
		r := httptest.NewRequest("POST", "http://localhost:13021/api/instances/schedule", strings.NewReader(string(body)))
		r.Header.Set("X-Control-Request", "1")
		out := httptest.NewRecorder()
		s.handler().ServeHTTP(out, r)
		if out.Code != 200 {
			t.Fatalf("%s failed: %d %s", action, out.Code, out.Body.String())
		}
	}
	sleep := time.Now().UTC().Add(time.Minute)
	send("set_sleep", &sleep)
	plans, _ := s.readPlans()
	p := plans["runtime1/instance"]
	if p.SleepAt == nil || !p.SleepAt.Equal(sleep) || p.DeleteAt == nil || !p.DeleteAt.Equal(nativeDelete) {
		t.Fatal("sleep plan replaced the original deletion")
	}
	send("cancel_delete", nil)
	plans, _ = s.readPlans()
	p = plans["runtime1/instance"]
	if p.DeleteAt != nil || p.SleepAt == nil || !p.SleepAt.Equal(sleep) {
		t.Fatal("cancel deletion affected sleep")
	}
	// Reload from disk, execute sleep, and later delete the paused instance.
	s2 := server{cfg: s.cfg, http: s.http}
	due := time.Now().Add(-time.Second)
	p.SleepAt = &due
	future := time.Now().Add(time.Hour)
	p.DeleteAt = &future
	plans["runtime1/instance"] = p
	if err := s.writePlans(plans); err != nil {
		t.Fatal(err)
	}
	s2.scheduleTick(context.Background())
	plans, _ = s.readPlans()
	p = plans["runtime1/instance"]
	if pauses != 1 || deletes != 0 || p.SleepAt != nil || p.DeleteAt == nil {
		t.Fatal("sleep failed or affected deletion")
	}
	p.DeleteAt = &due
	plans["runtime1/instance"] = p
	_ = s.writePlans(plans)
	s2.scheduleTick(context.Background())
	plans, _ = s.readPlans()
	if deletes != 1 || len(plans) != 0 {
		t.Fatal("paused instance was not deleted or stale plan survived")
	}
}

func TestCancelDeletionRenewsAndPersistsNoDeletion(t *testing.T) {
	renewals := 0
	end := time.Now().Add(time.Minute)
	rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var req struct {
				Timeout int `json:"timeout"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if req.Timeout != 1800 {
				t.Errorf("wrong renewal duration: %d", req.Timeout)
			}
			renewals++
			end = time.Now().Add(30 * time.Minute)
			w.WriteHeader(204)
			return
		}
		fmt.Fprintf(w, `{"sandboxID":"instance","state":"running","endAt":%q,"lifecycle":{"onTimeout":"kill"}}`, end.UTC().Format(time.RFC3339Nano))
	}))
	defer rt.Close()
	s := server{cfg: config{Data: t.TempDir(), Runtimes: []runtimeConfig{{Name: "runtime1", API: rt.URL}}}, http: http.DefaultClient}
	r := httptest.NewRequest("POST", "http://localhost:13021/api/instances/schedule", strings.NewReader(`{"runtime":"runtime1","sandbox_id":"instance","action":"cancel_delete"}`))
	r.Header.Set("X-Control-Request", "1")
	out := httptest.NewRecorder()
	s.handler().ServeHTTP(out, r)
	plans, err := s.readPlans()
	if out.Code != 200 || err != nil || renewals != 1 || plans["runtime1/instance"].DeleteAt != nil {
		t.Fatalf("cancel failed to renew: %d %s", out.Code, out.Body.String())
	}
	s.scheduleTick(context.Background())
	if renewals != 1 {
		t.Fatal("renewal shortened a sufficiently long deadline")
	}
}
