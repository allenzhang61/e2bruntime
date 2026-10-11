package e2e

import (
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type testConfig struct {
	URL                       string
	Timeout, BackupWait, Poll time.Duration
	Output                    string
}

// configInit lists every environment variable needed by the live suite. No Runtime/S3 keys are needed.
func configInit(t *testing.T) testConfig {
	t.Helper()
	if testing.Short() || os.Getenv("CONTROL_E2E") != "1" {
		t.Skip("set CONTROL_E2E=1 to run against the development Control server")
	}
	env := func(key, fallback string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		return fallback
	}
	duration := func(key, fallback string) time.Duration {
		d, err := time.ParseDuration(env(key, fallback))
		if err != nil || d < time.Second {
			t.Fatalf("invalid %s", key)
		}
		return d
	}
	c := testConfig{
		URL:        strings.TrimRight(env("CONTROL_E2E_URL", "http://localhost:13021"), "/"), // e.g. http://127.0.0.1:13021
		Timeout:    duration("CONTROL_E2E_TIMEOUT", "12m"),                                   // whole workflow including restore
		BackupWait: duration("CONTROL_E2E_BACKUP_WAIT", "4m"),                                // each periodic S3 backup wait
		Poll:       duration("CONTROL_E2E_POLL", "3s"),                                       // state/job polling interval
		Output:     env("CONTROL_E2E_OUTPUT", filepath.Join("out", time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+strconv.Itoa(os.Getpid()))),
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") || u.User != nil || u.RawQuery != "" || u.Path != "" {
		t.Fatal("CONTROL_E2E_URL must be a loopback HTTP Control URL")
	}
	return c
}
