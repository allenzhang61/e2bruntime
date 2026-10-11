package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Only scheduling and retention are editable; credentials and storage stay server-side.
type editableBackupSettings struct {
	Enabled         *bool `json:"enabled,omitempty"`
	IntervalSeconds int64 `json:"interval_seconds"`
	Keep            int   `json:"keep"`
}

func (s editableBackupSettings) validate() error {
	if s.IntervalSeconds < 10 || s.IntervalSeconds > 86400 || s.Keep < 1 || s.Keep > 1000 {
		return fmt.Errorf("interval_seconds must be 10..86400 and keep must be 1..1000")
	}
	return nil
}
func settingsPath(c config) string { return filepath.Join(filepath.Dir(c.ImportJobs), "settings.json") }
func loadBackupSettings(c config) (config, error) {
	raw, err := os.ReadFile(settingsPath(c))
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("backup settings unavailable")
	}
	var settings editableBackupSettings
	if json.Unmarshal(raw, &settings) != nil || settings.validate() != nil {
		return c, fmt.Errorf("invalid saved backup settings")
	}
	c.Interval = time.Duration(settings.IntervalSeconds) * time.Second
	c.Keep = settings.Keep
	if settings.Enabled != nil {
		c.BackupDisabled = !*settings.Enabled
	}
	return c, nil
}
func (w *worker) adminSettings(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "POST required", 405)
		return
	}
	var settings editableBackupSettings
	decoder := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&settings) != nil {
		http.Error(rw, "invalid settings", 400)
		return
	}
	if err := settings.validate(); err != nil {
		http.Error(rw, err.Error(), 400)
		return
	}
	if settings.Enabled == nil {
		cfg, err := loadBackupSettings(w.cfg)
		if err != nil {
			http.Error(rw, "backup settings unavailable", 500)
			return
		}
		enabled := !cfg.BackupDisabled
		settings.Enabled = &enabled
	}
	if err := saveJSON(settingsPath(w.cfg), settings); err != nil {
		http.Error(rw, "unable to persist backup settings", 500)
		return
	}
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(settings)
}
