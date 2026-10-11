package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type sandbox struct {
	AccessToken string            `json:"envdAccessToken"`
	ID          string            `json:"sandboxID"`
	Metadata    map[string]string `json:"metadata"`
}
type api struct {
	base, proxy, key string
	http             *http.Client
}

func newAPI(c config) *api {
	return &api{strings.TrimRight(c.API, "/"), strings.TrimRight(c.Proxy, "/"), c.Key, &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (a *api) call(ctx context.Context, method, path string, body any, result any) (string, error) {
	var b io.Reader
	if body != nil {
		data, e := json.Marshal(body)
		if e != nil {
			return "", e
		}
		b = bytes.NewReader(data)
	}
	r, e := http.NewRequestWithContext(ctx, method, a.base+path, b)
	if e != nil {
		return "", e
	}
	r.Header.Set("X-API-Key", a.key)
	r.Header.Set("Content-Type", "application/json")
	res, e := a.http.Do(r)
	if e != nil {
		return "", fmt.Errorf("API request failed (%T)", e)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("API %s %s HTTP %d", method, strings.Split(path, "?")[0], res.StatusCode)
	}
	if result != nil {
		if e = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(result); e != nil {
			return "", e
		}
	}
	return res.Header.Get("X-Next-Token"), nil
}
func (a *api) running(ctx context.Context) ([]sandbox, error) {
	var all []sandbox
	token := ""
	seen := map[string]bool{}
	for {
		p := "/v2/sandboxes?state=running&limit=100"
		if token != "" {
			p += "&nextToken=" + url.QueryEscape(token)
		}
		var page []sandbox
		next, e := a.call(ctx, "GET", p, nil, &page)
		if e != nil {
			return nil, e
		}
		all = append(all, page...)
		if next == "" {
			return all, nil
		}
		if seen[next] {
			return nil, fmt.Errorf("repeated pagination token")
		}
		seen[next] = true
		token = next
	}
}
func (a *api) snapshot(ctx context.Context, id string) (string, string, error) {
	var s struct {
		ID string `json:"snapshotID"`
	}
	_, e := a.call(ctx, "POST", "/sandboxes/"+url.PathEscape(id)+"/snapshots", map[string]bool{"memory": false}, &s)
	if e != nil {
		return "", "", e
	}
	tmpl, _, _ := strings.Cut(s.ID, ":")
	if tmpl == "" {
		return "", "", fmt.Errorf("missing snapshot ID")
	}
	var result struct {
		Builds []struct {
			ID     string `json:"buildID"`
			Status string `json:"status"`
		} `json:"builds"`
	}
	_, e = a.call(ctx, "GET", "/templates/"+url.PathEscape(tmpl), nil, &result)
	if e != nil {
		return s.ID, "", e
	}
	for _, b := range result.Builds {
		if b.Status == "ready" && uuidPattern.MatchString(b.ID) {
			return s.ID, b.ID, nil
		}
	}
	return s.ID, "", fmt.Errorf("no ready build")
}
func (a *api) deleteSnapshot(ctx context.Context, id string) error {
	tmpl, _, _ := strings.Cut(id, ":")
	_, e := a.call(ctx, "DELETE", "/templates/"+url.PathEscape(tmpl), nil, nil)
	return e
}

// sandboxState distinguishes a missing instance from API/auth/network failures.
// It does not connect or wake a sandbox.
func (a *api) sandboxState(ctx context.Context, id string) string {
	if !sandboxIDPattern.MatchString(id) {
		return "unknown"
	}
	req, err := http.NewRequestWithContext(ctx, "GET", a.base+"/sandboxes/"+url.PathEscape(id), nil)
	if err != nil {
		return "unknown"
	}
	req.Header.Set("X-API-Key", a.key)
	response, err := a.http.Do(req)
	if err != nil {
		return "unknown"
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return "missing"
	}
	if response.StatusCode != http.StatusOK {
		return "unknown"
	}
	var info struct {
		ID    string `json:"sandboxID"`
		State string `json:"state"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&info) != nil || info.ID != id {
		return "unknown"
	}
	if info.State == "running" || info.State == "paused" {
		return info.State
	}
	return "unknown"
}
