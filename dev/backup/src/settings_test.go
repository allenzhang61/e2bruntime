package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEditableBackupSettingsPersistenceAndAccess(t *testing.T) {
	c := config{Key: "secret", Node: "runtime2", ImportJobs: filepath.Join(t.TempDir(), "import-jobs"), Interval: 120 * time.Second, Keep: 10, Bucket: "original-bucket"}
	w := worker{cfg: c}
	send := func(body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost/settings", strings.NewReader(body))
		r.Header.Set("X-API-Key", key)
		out := httptest.NewRecorder()
		w.adminHandler().ServeHTTP(out, r)
		return out
	}
	for _, tc := range []struct {
		body, key string
		code      int
	}{
		{`{"interval_seconds":60,"keep":5}`, "", 401},
		{`{"interval_seconds":9,"keep":5}`, "secret", 400},
		{`{"interval_seconds":86401,"keep":5}`, "secret", 400},
		{`{"interval_seconds":60,"keep":0}`, "secret", 400},
		{`{"interval_seconds":60,"keep":1001}`, "secret", 400},
		{`{"interval_seconds":60,"keep":5,"bucket":"other"}`, "secret", 400},
	} {
		if out := send(tc.body, tc.key); out.Code != tc.code {
			t.Fatalf("got %d want %d", out.Code, tc.code)
		}
	}
	if _, err := os.Stat(settingsPath(c)); !os.IsNotExist(err) {
		t.Fatal("invalid request wrote settings")
	}
	if out := send(`{"interval_seconds":60,"keep":5}`, "secret"); out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	loaded, err := loadBackupSettings(c)
	if err != nil || loaded.Interval != time.Minute || loaded.Keep != 5 || loaded.Bucket != c.Bucket || loaded.Key != c.Key {
		t.Fatalf("settings not loaded safely: %v", err)
	}
	info, err := os.Stat(settingsPath(c))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("settings not private")
	}
	// A separately constructed admin worker sees new configuration while stopped.
	fresh := worker{cfg: c}
	out := httptest.NewRecorder()
	fresh.adminSchedule(out, httptest.NewRequest("GET", "http://localhost/schedule", nil))
	var state struct {
		Status string         `json:"status"`
		Config backupSettings `json:"config"`
	}
	if json.Unmarshal(out.Body.Bytes(), &state) != nil || state.Status != "stopped" || state.Config.Keep != 5 || state.Config.IntervalSeconds != 60 {
		t.Fatal(out.Body.String())
	}
	if err = os.WriteFile(settingsPath(c), []byte(`{"keep":5}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadBackupSettings(c); err == nil {
		t.Fatal("corrupt configuration silently accepted")
	}
}

func TestBackupSwitchPersistsAndOldClientsPreserveIt(t *testing.T) {
	c := config{ImportJobs: filepath.Join(t.TempDir(), "jobs"), Interval: time.Minute, Keep: 10}
	w := worker{cfg: c}
	for _, tc := range []struct {
		body     string
		disabled bool
	}{
		{`{"interval_seconds":60,"keep":10,"enabled":false}`, true},
		{`{"interval_seconds":120,"keep":10}`, true},
		{`{"interval_seconds":120,"keep":10,"enabled":true}`, false},
	} {
		out := httptest.NewRecorder()
		w.adminSettings(out, httptest.NewRequest("POST", "/settings", strings.NewReader(tc.body)))
		if out.Code != 200 {
			t.Fatal(out.Code, out.Body.String())
		}
		fresh, err := loadBackupSettings(c)
		if err != nil || fresh.BackupDisabled != tc.disabled {
			t.Fatalf("switch not persisted: %v", err)
		}
		out = httptest.NewRecorder()
		w.adminSchedule(out, httptest.NewRequest("GET", "/schedule", nil))
		var state struct {
			Status string         `json:"status"`
			Next   time.Time      `json:"next_cycle_at"`
			Config backupSettings `json:"config"`
		}
		if json.Unmarshal(out.Body.Bytes(), &state) != nil || state.Config.Enabled == tc.disabled {
			t.Fatal(out.Body.String())
		}
		if tc.disabled && (state.Status != "disabled" || !state.Next.IsZero()) {
			t.Fatal("disabled task retained a schedule")
		}
	}
}
