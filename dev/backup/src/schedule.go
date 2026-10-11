package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type backupSettings struct {
	Enabled         bool   `json:"enabled"`
	Node            string `json:"node"`
	IntervalSeconds int64  `json:"interval_seconds"`
	Keep            int    `json:"keep"`
	Bucket          string `json:"bucket"`
	Kind            string `json:"kind"`
	ArchivePath     string `json:"archive_path"`
}

type backupSchedule struct {
	Process   int       `json:"pid"`
	Status    string    `json:"status"`
	Next      time.Time `json:"next_cycle_at"`
	Current   string    `json:"current_instance_id,omitempty"`
	Completed []string  `json:"processed_instances,omitempty"`
}

func (w *worker) schedulePath() string {
	return filepath.Join(filepath.Dir(w.cfg.ImportJobs), "schedule.json")
}
func (w *worker) publishSchedule(state backupSchedule) {
	state.Process = os.Getpid()
	raw, err := json.Marshal(state)
	if err != nil {
		return
	}
	path := w.schedulePath()
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), "schedule-")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return
	}
	if f.Close() == nil {
		_ = os.Rename(f.Name(), path)
	}
}
func (w *worker) adminSchedule(rw http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(rw, "GET required", 405)
		return
	}
	cfg, err := loadBackupSettings(w.cfg)
	if err != nil {
		http.Error(rw, "backup settings unavailable", 500)
		return
	}
	state := backupSchedule{Status: "stopped"}
	raw, err := os.ReadFile(w.schedulePath())
	if err == nil {
		if json.Unmarshal(raw, &state) != nil || state.Process <= 0 || syscall.Kill(state.Process, 0) != nil {
			state = backupSchedule{Status: "stopped"}
		}
	} else if !os.IsNotExist(err) {
		state.Status = "unknown"
	}
	if cfg.BackupDisabled && state.Status != "cycling" {
		state.Status = "disabled"
		state.Next = time.Time{}
	}
	state.Process = 0
	settings := backupSettings{Enabled: !cfg.BackupDisabled, Node: cfg.Node, IntervalSeconds: int64(cfg.Interval / time.Second), Keep: cfg.Keep, Bucket: w.cfg.Bucket, Kind: "filesystem", ArchivePath: "backups/<Logic ID>/" + w.cfg.Node + "/<Instance ID>/filesystem/"}
	response := struct {
		backupSchedule
		Config backupSettings `json:"config"`
	}{state, settings}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(response)
}
