package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScheduleReportsLiveWorkerAndMissingState(t *testing.T) {
	w := worker{cfg: config{ImportJobs: filepath.Join(t.TempDir(), "import-jobs"), Node: "runtime1", Interval: 2 * time.Minute, Keep: 10, Bucket: "test", AK: "secret-ak", SK: "secret-sk", Key: "secret-api"}}
	req := httptest.NewRequest("GET", "http://localhost/schedule", nil)
	out := httptest.NewRecorder()
	w.adminSchedule(out, req)
	var state backupSchedule
	if json.Unmarshal(out.Body.Bytes(), &state) != nil || state.Status != "stopped" {
		t.Fatal("missing worker schedule not marked stopped")
	}
	var response struct {
		Config backupSettings `json:"config"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil || response.Config.IntervalSeconds != 120 || response.Config.Keep != 10 || response.Config.Node != "runtime1" {
		t.Fatalf("missing stopped-worker configuration: %s", out.Body.String())
	}
	if strings.Contains(out.Body.String(), "secret") {
		t.Fatal("credentials exposed")
	}
	due := time.Now().UTC().Add(2 * time.Minute)
	w.publishSchedule(backupSchedule{Status: "cycling", Next: due, Current: "instance", Completed: []string{"previous"}})
	out = httptest.NewRecorder()
	w.adminSchedule(out, req)
	if json.Unmarshal(out.Body.Bytes(), &state) != nil || state.Status != "cycling" || state.Current != "instance" || !state.Next.Equal(due) || len(state.Completed) != 1 {
		t.Fatalf("lost actual scheduler state: %s", out.Body.String())
	}
}
