package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type measurement struct {
	Name  string  `json:"name"`
	MS    float64 `json:"elapsed_ms"`
	Error string  `json:"error,omitempty"`
}
type suite struct {
	cfg     testConfig
	ctx     context.Context
	http    *http.Client
	steps   []measurement
	logic   string
	created map[string]string
}

func (s *suite) call(method, path string, body, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(s.ctx, method, s.cfg.URL+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Control-Request", "1")
	resp, err := s.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s HTTP %d: %s", method, path, resp.StatusCode, string(raw))
	}
	if out != nil {
		if err = json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}
func (s *suite) must(t *testing.T, method, path string, body, out any) {
	t.Helper()
	if _, err := s.call(method, path, body, out); err != nil {
		t.Fatal(err)
	}
}
func (s *suite) step(t *testing.T, name string, run func()) {
	t.Helper()
	start := time.Now()
	done := false
	defer func() {
		m := measurement{Name: name, MS: float64(time.Since(start).Microseconds()) / 1000}
		if !done {
			m.Error = "step failed; see Go test output"
		}
		s.steps = append(s.steps, m)
		t.Logf("%s: %.2f ms", name, m.MS)
	}()
	run()
	done = true
}
func (s *suite) wait(t *testing.T, limit time.Duration, description string, condition func() (bool, error)) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for {
		ok, err := condition()
		if err != nil {
			t.Fatal(description, err)
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", description)
		}
		select {
		case <-s.ctx.Done():
			t.Fatal(s.ctx.Err())
		case <-time.After(s.cfg.Poll):
		}
	}
}
func (s *suite) debug(t *testing.T, node, action string, args map[string]any) map[string]any {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	args["runtime"] = node
	var response struct {
		Success bool           `json:"success"`
		Result  map[string]any `json:"result"`
	}
	s.must(t, "POST", "/api/debug/"+action, args, &response)
	if !response.Success {
		t.Fatalf("%s failed", action)
	}
	return response.Result
}
func (s *suite) report(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(s.cfg.Output, 0755); err != nil {
		t.Error(err)
		return
	}
	raw, err := json.MarshalIndent(map[string]any{"passed": !t.Failed(), "logic_id": s.logic, "test_instances": s.created, "steps": s.steps, "finished_at_utc": time.Now().UTC()}, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(s.cfg.Output, "unified-view.json"), append(raw, '\n'), 0644)
	}
	if err != nil {
		t.Error(err)
	}
	t.Log("report:", filepath.Join(s.cfg.Output, "unified-view.json"))
}
