// Package clientgo exercises the same public REST/file APIs used by E2B SDKs.
package clientgo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	APIURL, ProxyURL, APIKey string
	HTTP                     *http.Client
}

type Sandbox struct {
	ID          string `json:"sandboxID"`
	AccessToken string `json:"envdAccessToken"`
}

func NewClient(api, proxy, key string) *Client {
	return &Client{strings.TrimRight(api, "/"), strings.TrimRight(proxy, "/"), key,
		&http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Do not put response bodies in errors: API replies may contain credentials.
func (c *Client) request(ctx context.Context, method, target, contentType string, body io.Reader, sandbox *Sandbox) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if sandbox == nil {
		req.Header.Set("X-API-Key", c.APIKey)
	} else {
		req.Header.Set("E2b-Sandbox-Id", sandbox.ID)
		req.Header.Set("E2b-Sandbox-Port", "49983")
		req.Header.Set("X-Access-Token", sandbox.AccessToken)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return nil, res.StatusCode, err
	}
	if len(data) > 1<<20 {
		return nil, res.StatusCode, fmt.Errorf("response exceeds 1 MiB")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, res.StatusCode, fmt.Errorf("%s %s: HTTP %d", method, req.URL.Path, res.StatusCode)
	}
	return data, res.StatusCode, nil
}

func (c *Client) Create(ctx context.Context) (Sandbox, error) {
	return c.CreateFromTemplate(ctx, "base")
}

func (c *Client) CreateFromTemplate(ctx context.Context, template string) (Sandbox, error) {
	var sb Sandbox
	body, err := json.Marshal(map[string]any{"templateID": template, "timeout": 120, "metadata": map[string]string{"purpose": "dev-go-e2e"}})
	if err != nil {
		return sb, err
	}
	data, _, err := c.request(ctx, http.MethodPost, c.APIURL+"/v2/sandboxes", "application/json", bytes.NewReader(body), nil)
	if err != nil {
		return sb, err
	}
	err = json.Unmarshal(data, &sb)
	return sb, err
}

func (c *Client) Get(ctx context.Context, id string) (Sandbox, int, error) {
	var sb Sandbox
	data, status, err := c.request(ctx, http.MethodGet, c.APIURL+"/sandboxes/"+url.PathEscape(id), "", nil, nil)
	if err == nil {
		err = json.Unmarshal(data, &sb)
	}
	return sb, status, err
}

func (c *Client) Delete(ctx context.Context, id string) error {
	_, _, err := c.request(ctx, http.MethodDelete, c.APIURL+"/sandboxes/"+url.PathEscape(id), "", nil, nil)
	return err
}

func (c *Client) WriteFile(ctx context.Context, sb Sandbox, path string, data []byte) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "e2e.txt")
	if err != nil {
		return err
	}
	if _, err = part.Write(data); err != nil {
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	_, _, err = c.request(ctx, http.MethodPost, c.ProxyURL+"/files?path="+url.QueryEscape(path), writer.FormDataContentType(), &body, &sb)
	return err
}

func (c *Client) ReadFile(ctx context.Context, sb Sandbox, path string) ([]byte, error) {
	data, _, err := c.request(ctx, http.MethodGet, c.ProxyURL+"/files?path="+url.QueryEscape(path), "", nil, &sb)
	return data, err
}

// FilesystemSnapshot deliberately excludes memory and leaves the source running.
func (c *Client) FilesystemSnapshot(ctx context.Context, id string) (string, error) {
	data, _, err := c.request(ctx, http.MethodPost, c.APIURL+"/sandboxes/"+url.PathEscape(id)+"/snapshots", "application/json", strings.NewReader(`{"memory":false}`), nil)
	if err != nil {
		return "", err
	}
	var result struct {
		ID string `json:"snapshotID"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	if result.ID == "" {
		return "", fmt.Errorf("snapshot response has no snapshotID")
	}
	return result.ID, nil
}
func (c *Client) DeleteSnapshot(ctx context.Context, id string) error {
	// Untagged generated snapshots use <templateID>:default.
	template, _, _ := strings.Cut(id, ":")
	_, _, err := c.request(ctx, http.MethodDelete, c.APIURL+"/templates/"+url.PathEscape(template), "", nil, nil)
	return err
}
