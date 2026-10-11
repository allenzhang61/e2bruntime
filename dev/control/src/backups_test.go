package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBackupServicesPartialFailureAndRedaction(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/schedule" || r.Header.Get("X-API-Key") != "secret-api" {
			t.Error("invalid node request")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"stopped","pid":42,"config":{"node":"runtime1","interval_seconds":120,"keep":10,"bucket":"test","archive_path":"backups/<Logic ID>/runtime1/<Instance ID>/filesystem/","access_key":"secret-ak"}}`))
	}))
	defer node.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "secret-upstream", 500) }))
	defer bad.Close()
	s := server{cfg: config{Runtimes: []runtimeConfig{{Name: "runtime1", Admin: node.URL, Key: "secret-api"}, {Name: "runtime2", Admin: bad.URL}}}, http: http.DefaultClient}
	out := httptest.NewRecorder()
	s.handler().ServeHTTP(out, httptest.NewRequest("GET", "http://localhost:13021/api/backups", nil))
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	var data struct {
		Runtimes []backupServiceView `json:"runtimes"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Runtimes) != 2 || data.Runtimes[0].Status != "stopped" || data.Runtimes[0].Config == nil || data.Runtimes[0].Config.Keep != 10 || data.Runtimes[1].Status != "unreachable" {
		t.Fatal(out.Body.String())
	}
	if strings.Contains(out.Body.String(), "secret") || strings.Contains(out.Body.String(), "pid") {
		t.Fatal("leaked non-allowlisted fields")
	}
}
