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

func TestConfigureBackupServiceForwardsOnlyValidatedSettings(t *testing.T) {
	calls := 0
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/settings" || r.Header.Get("X-API-Key") != "secret" {
			t.Error("invalid upstream request")
		}
		var body map[string]int
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 2 || body["keep"] != 7 || body["interval_seconds"] != 180 {
			t.Error("unexpected settings payload")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"keep":7,"interval_seconds":180}`))
	}))
	defer node.Close()
	s := server{cfg: config{Runtimes: []runtimeConfig{{Name: "runtime2", Admin: node.URL, Key: "secret"}}}, http: http.DefaultClient}
	router := s.handler()
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"runtime":"runtime2","interval_seconds":180,"keep":7}`, 200},
		{`{"runtime":"runtime2","interval_seconds":1,"keep":7}`, 400},
		{`{"runtime":"unknown","interval_seconds":180,"keep":7}`, 400},
	} {
		r := httptest.NewRequest("POST", "http://localhost:13021/api/backups/config", strings.NewReader(tc.body))
		r.Header.Set("X-Control-Request", "1")
		out := httptest.NewRecorder()
		router.ServeHTTP(out, r)
		if out.Code != tc.code {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("invalid settings forwarded: %d calls", calls)
	}
}
