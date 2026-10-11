package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type instancePlan struct {
	Runtime  string     `json:"runtime"`
	ID       string     `json:"sandbox_id"`
	SleepAt  *time.Time `json:"sleep_at"`
	DeleteAt *time.Time `json:"delete_at"`
	Error    string     `json:"error,omitempty"`
}
type instanceInfo struct {
	ID        string    `json:"sandboxID"`
	State     string    `json:"state"`
	End       time.Time `json:"endAt"`
	Lifecycle struct {
		OnTimeout string `json:"onTimeout"`
	} `json:"lifecycle"`
}

// Serializes plan changes and scheduler side effects, so a canceled plan cannot
// race a previously copied due action. The existing jobs lock is independent.
var planMu sync.Mutex

func (s *server) readPlans() (map[string]instancePlan, error) {
	plans := map[string]instancePlan{}
	raw, err := os.ReadFile(filepath.Join(s.cfg.Data, "instance-plans.json"))
	if os.IsNotExist(err) {
		return plans, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &plans) != nil || plans == nil {
		return nil, fmt.Errorf("invalid instance plans")
	}
	for key, p := range plans {
		if key != p.Runtime+"/"+p.ID || !debugIDPattern.MatchString(p.ID) || s.runtimeByName(p.Runtime).Name == "" {
			return nil, fmt.Errorf("invalid instance plan identity")
		}
	}
	return plans, nil
}
func (s *server) writePlans(plans map[string]instancePlan) error {
	raw, err := json.MarshalIndent(plans, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.cfg.Data, "plans-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(s.cfg.Data, "instance-plans.json"))
}
func (s *server) runtimeByName(name string) runtimeConfig {
	for _, rt := range s.cfg.Runtimes {
		if rt.Name == name {
			return rt
		}
	}
	return runtimeConfig{}
}
func (s *server) configureSchedule(c *gin.Context) {
	var req struct {
		Runtime string     `json:"runtime" binding:"required"`
		ID      string     `json:"sandbox_id" binding:"required"`
		Action  string     `json:"action" binding:"required,oneof=set_sleep cancel_sleep set_delete cancel_delete"`
		At      *time.Time `json:"at"`
	}
	if !bindJSON(c, &req, 4096, "invalid schedule request") {
		return
	}
	rt := s.runtimeByName(req.Runtime)
	if rt.Name == "" || !debugIDPattern.MatchString(req.ID) {
		apiError(c, 400, "valid Runtime and instance required")
		return
	}
	if req.Action == "set_sleep" || req.Action == "set_delete" {
		if req.At == nil || !req.At.After(time.Now().Add(10*time.Second)) {
			apiError(c, 400, "choose an execution time more than 10 seconds in the future")
			return
		}
	}
	planMu.Lock()
	defer planMu.Unlock()
	plans, err := s.readPlans()
	if err != nil {
		apiError(c, 500, "unable to read instance plans")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	var info instanceInfo
	path := "/sandboxes/" + url.PathEscape(req.ID)
	if _, err = s.call(ctx, rt, false, "GET", path, nil, &info); err != nil {
		apiError(c, 502, err.Error())
		return
	}
	if info.ID != req.ID || (info.State != "running" && info.State != "paused") {
		apiError(c, 409, "instance is not running or paused")
		return
	}
	if req.Action == "set_sleep" && info.State != "running" {
		apiError(c, 409, "wake the instance before scheduling sleep")
		return
	}
	key := req.Runtime + "/" + req.ID
	plan, exists := plans[key]
	if !exists {
		plan = instancePlan{Runtime: req.Runtime, ID: req.ID}
		// Keep the other existing native policy when taking over scheduling.
		if info.State == "running" && !info.End.IsZero() {
			at := info.End
			if info.Lifecycle.OnTimeout == "pause" {
				plan.SleepAt = &at
			}
			if info.Lifecycle.OnTimeout == "kill" {
				plan.DeleteAt = &at
			}
		}
	}
	switch req.Action {
	case "set_sleep":
		plan.SleepAt = req.At
	case "cancel_sleep":
		plan.SleepAt = nil
	case "set_delete":
		plan.DeleteAt = req.At
	case "cancel_delete":
		plan.DeleteAt = nil
	}
	plan.Error = ""
	plans[key] = plan
	if err = s.writePlans(plans); err != nil {
		apiError(c, 500, "unable to persist instance plan")
		return
	}
	// Persist before renewal so a restart never loses the user's intent.
	if info.State == "running" {
		if err = s.renewManaged(ctx, rt, info); err != nil {
			plan.Error = "plan saved but renewal failed: " + err.Error()
			plans[key] = plan
			_ = s.writePlans(plans)
			apiError(c, 502, plan.Error)
			return
		}
	}
	c.JSON(http.StatusOK, plan)
}
func (s *server) renewManaged(ctx context.Context, rt runtimeConfig, info instanceInfo) error {
	if info.End.After(time.Now().Add(5 * time.Minute)) {
		return nil
	}
	_, err := s.call(ctx, rt, false, "POST", "/sandboxes/"+url.PathEscape(info.ID)+"/timeout", gin.H{"timeout": 1800}, nil)
	return err
}
func (s *server) scheduleLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		s.scheduleTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *server) scheduleTick(ctx context.Context) {
	planMu.Lock()
	defer planMu.Unlock()
	plans, err := s.readPlans()
	if err != nil {
		return
	}
	changed := false
	for key, plan := range plans {
		if ctx.Err() != nil {
			break
		}
		rt := s.runtimeByName(plan.Runtime)
		operation, cancel := context.WithTimeout(ctx, 20*time.Second)
		var info instanceInfo
		path := "/sandboxes/" + url.PathEscape(plan.ID)
		_, err = s.call(operation, rt, false, "GET", path, nil, &info)
		if err != nil && strings.HasPrefix(err.Error(), rt.Name+" HTTP 404:") {
			delete(plans, key)
			changed = true
			cancel()
			continue
		}
		if err == nil && info.ID != plan.ID {
			err = fmt.Errorf("instance identity mismatch")
		}
		now := time.Now()
		if err == nil {
			switch {
			case plan.DeleteAt != nil && !plan.DeleteAt.After(now):
				_, err = s.call(operation, rt, false, "DELETE", path, nil, nil)
				if err == nil {
					delete(plans, key)
					changed = true
					cancel()
					continue
				}
			case plan.SleepAt != nil && !plan.SleepAt.After(now):
				if info.State == "running" {
					_, err = s.call(operation, rt, false, "POST", path+"/pause", gin.H{"memory": true}, nil)
				} else if info.State != "paused" {
					err = fmt.Errorf("instance is changing state")
				}
				if err == nil {
					plan.SleepAt = nil
				}
			case info.State == "running":
				err = s.renewManaged(operation, rt, info)
			}
		}
		cancel()
		previous := plan.Error
		if err != nil {
			plan.Error = err.Error()
		} else {
			plan.Error = ""
		}
		if plan.Error != previous || err == nil {
			plans[key] = plan
			changed = true
		}
	}
	if changed {
		_ = s.writePlans(plans)
	}
}
