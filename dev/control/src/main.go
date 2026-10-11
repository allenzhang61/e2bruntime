package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gin-gonic/gin"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed index.html debug.html backups.html logic_instances.html logic_backups.html refresh.html header.html
var pages embed.FS

type job struct {
	ID       string `json:"id"`
	LogicID  string `json:"logic_id"`
	Target   string `json:"target_runtime"`
	Archive  string `json:"archive_key"`
	Instance string `json:"instance_id,omitempty"`
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
	Created  string `json:"created_at_utc"`
	Finished string `json:"finished_at_utc,omitempty"`
}
type server struct {
	cfg  config
	s3   *s3.Client
	http *http.Client
	mu   sync.Mutex
	busy bool
	jobs []job
}

func (s *server) call(ctx context.Context, rt runtimeConfig, admin bool, method, path string, body, out any) (string, error) {
	base := rt.API
	if admin {
		base = rt.Admin
	}
	var reader io.Reader
	if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return "", e
		}
		reader = bytes.NewReader(raw)
	}
	timeout := 8 * time.Second
	if method == "POST" {
		timeout = 30 * time.Second
	}
	limited, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, e := http.NewRequestWithContext(limited, method, base+path, reader)
	if e != nil {
		return "", e
	}
	req.Header.Set("X-API-Key", rt.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, e := s.http.Do(req)
	if e != nil {
		return "", fmt.Errorf("%s service unavailable", rt.Name)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("%s HTTP %d: %s", rt.Name, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out != nil {
		if e = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); e != nil {
			return "", fmt.Errorf("invalid Runtime response")
		}
	}
	return resp.Header.Get("X-Next-Token"), nil
}
func (s *server) persist() error {
	b, e := json.MarshalIndent(s.jobs, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(s.cfg.Data, "jobs-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(f.Name(), filepath.Join(s.cfg.Data, "jobs.json"))
}
func (s *server) update(id, status, instanceID string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.jobs {
		if s.jobs[i].ID == id {
			s.jobs[i].Status = status
			s.jobs[i].Instance = instanceID
			if err != nil {
				s.jobs[i].Error = err.Error()
			} else {
				s.jobs[i].Error = ""
			}
			if status == "succeeded" || status == "failed" {
				s.jobs[i].Finished = time.Now().UTC().Format(time.RFC3339)
				s.busy = false
			}
		}
	}
	if e := s.persist(); e != nil {
		log.Print("job persistence failed")
	}
}
func (s *server) run(j job, rt runtimeConfig, img image) {
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	entry := map[string]any{"schema_version": 1, "source_node": img.Node, "source_sandbox_id": img.Instance, "backup_at_utc": img.At, "bucket": s.cfg.Bucket, "archive_key": img.Archive, "metadata_key": img.MetadataKey, "metadata": img.Raw, "importable": true}
	s.update(j.ID, "preparing", "", nil)
	var views []struct {
		Owned bool   `json:"owned"`
		Error string `json:"error"`
		Owner *owner `json:"owner"`
	}
	if _, e := s.call(ctx, rt, true, "POST", "/ownership", []any{entry}, &views); e != nil {
		s.update(j.ID, "failed", "", e)
		return
	}
	if len(views) != 1 || views[0].Error != "" || !views[0].Owned || views[0].Owner == nil || views[0].Owner.Node != rt.Name {
		s.update(j.ID, "failed", "", fmt.Errorf("ownership lookup failed"))
		return
	}
	if _, e := s.call(ctx, rt, true, "POST", "/prepare-import", []any{entry}, &views); e != nil {
		s.update(j.ID, "failed", "", e)
		return
	}
	if len(views) != 1 || views[0].Error != "" || views[0].Owner == nil {
		s.update(j.ID, "failed", "", fmt.Errorf("prepare import rejected"))
		return
	}
	entry["ownership_generation"] = views[0].Owner.Generation

	var imported struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Results []struct {
			Status string `json:"status"`
			ID     string `json:"sandbox_id"`
			Error  string `json:"error"`
		} `json:"results"`
	}
	if _, e := s.call(ctx, rt, true, "POST", "/imports", []any{entry}, &imported); e != nil {
		s.update(j.ID, "failed", "", e)
		return
	}
	s.update(j.ID, "restoring", "", nil)
	for {
		if imported.Status == "completed" || imported.Status == "completed_with_errors" || imported.Status == "interrupted" {
			if len(imported.Results) == 1 && (imported.Results[0].Status == "succeeded" || imported.Results[0].Status == "existing") {
				var live struct {
					ID    string `json:"sandboxID"`
					State string `json:"state"`
				}
				id := imported.Results[0].ID
				if _, e := s.call(ctx, rt, false, "GET", "/sandboxes/"+url.PathEscape(id), nil, &live); e != nil || live.ID != id || (live.State != "running" && live.State != "paused") {
					s.update(j.ID, "failed", id, fmt.Errorf("import returned success but target instance is unavailable"))
					return
				}
				s.update(j.ID, "succeeded", id, nil)
				return
			}
			message := "import incomplete; inspect target before retry"
			if len(imported.Results) > 0 && imported.Results[0].Error != "" {
				message = imported.Results[0].Error
			}
			s.update(j.ID, "failed", "", fmt.Errorf("%s", message))
			return
		}
		select {
		case <-ctx.Done():
			s.update(j.ID, "failed", "", fmt.Errorf("import polling timed out; inspect target before retry"))
			return
		case <-time.After(2 * time.Second):
		}
		if _, e := s.call(ctx, rt, true, "GET", "/imports?id="+url.QueryEscape(imported.ID), nil, &imported); e != nil {
			s.update(j.ID, "failed", "", e)
			return
		}
	}
}
func main() {
	gin.SetMode(gin.ReleaseMode)
	c, e := configInit()
	if e != nil {
		log.Fatal(e)
	}
	if e = os.MkdirAll(c.Data, 0700); e != nil {
		log.Fatal(e)
	}
	s := &server{cfg: c, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, jobs: []job{}, s3: s3.NewFromConfig(aws.Config{Region: c.Region, Credentials: credentials.NewStaticCredentialsProvider(c.AK, c.SK, "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(c.Endpoint); o.UsePathStyle = true })}
	if b, e := os.ReadFile(filepath.Join(c.Data, "jobs.json")); e == nil {
		if json.Unmarshal(b, &s.jobs) != nil {
			log.Fatal("invalid persisted jobs")
		}
		for i := range s.jobs {
			if (s.jobs[i].Status != "succeeded" && s.jobs[i].Status != "failed") || s.jobs[i].Error == "control service restarted; inspect target before retry" {
				s.jobs[i].Status = "reconciling"
				s.jobs[i].Error = ""
			}
		}
		if e = s.persist(); e != nil {
			log.Fatal(e)
		}
	} else if !os.IsNotExist(e) {
		log.Fatal(e)
	}
	for _, pending := range s.jobs {
		if pending.Status == "reconciling" {
			s.busy = true
			go s.reconcile(pending)
			break
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go s.scheduleLoop(ctx)
	h := &http.Server{Addr: c.Listen, Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		h.Shutdown(shutdown)
	}()
	log.Printf("Logic control listening on %s", c.Listen)
	if e = h.ListenAndServe(); e != nil && e != http.ErrServerClosed {
		log.Fatal(e)
	}
}
