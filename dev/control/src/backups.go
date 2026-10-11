package main

import (
	"context"
	"github.com/gin-gonic/gin"
	"net/http"
	"sync"
	"time"
)

type backupServiceView struct {
	Runtime   string    `json:"runtime"`
	Status    string    `json:"status"`
	Next      time.Time `json:"next_cycle_at"`
	Current   string    `json:"current_instance_id,omitempty"`
	Processed []string  `json:"processed_instances,omitempty"`
	Config    *struct {
		Enabled         bool   `json:"enabled"`
		Node            string `json:"node"`
		IntervalSeconds int64  `json:"interval_seconds"`
		Keep            int    `json:"keep"`
		Bucket          string `json:"bucket"`
		Kind            string `json:"kind"`
		ArchivePath     string `json:"archive_path"`
	} `json:"config,omitempty"`
	Error string `json:"error,omitempty"`
}

func (s *server) listBackupServices(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	views := make([]backupServiceView, len(s.cfg.Runtimes))
	var wg sync.WaitGroup
	for index, rt := range s.cfg.Runtimes {
		wg.Add(1)
		go func(index int, rt runtimeConfig) {
			defer wg.Done()
			var view backupServiceView
			if _, err := s.call(ctx, rt, true, "GET", "/schedule", nil, &view); err != nil {
				view = backupServiceView{Status: "unreachable", Error: "无法访问备份查询服务"}
			}
			view.Runtime = rt.Name
			views[index] = view
		}(index, rt)
	}
	wg.Wait()
	c.JSON(http.StatusOK, gin.H{"checked_at_utc": time.Now().UTC().Format(time.RFC3339), "runtimes": views})
}

func (s *server) configureBackupService(c *gin.Context) {
	var req struct {
		Enabled         *bool  `json:"enabled"`
		Runtime         string `json:"runtime" binding:"required"`
		IntervalSeconds int64  `json:"interval_seconds" binding:"required,min=10,max=86400"`
		Keep            int    `json:"keep" binding:"required,min=1,max=1000"`
	}
	if !bindJSON(c, &req, 4096, "间隔须为 10–86400 秒，保留数量须为 1–1000") {
		return
	}
	var target *runtimeConfig
	for i := range s.cfg.Runtimes {
		if s.cfg.Runtimes[i].Name == req.Runtime {
			target = &s.cfg.Runtimes[i]
			break
		}
	}
	if target == nil {
		apiError(c, http.StatusBadRequest, "unknown Runtime")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	var result struct {
		Enabled         bool  `json:"enabled"`
		IntervalSeconds int64 `json:"interval_seconds"`
		Keep            int   `json:"keep"`
	}
	payload := gin.H{"interval_seconds": req.IntervalSeconds, "keep": req.Keep}
	if req.Enabled != nil {
		payload["enabled"] = *req.Enabled
	}
	if _, err := s.call(ctx, *target, true, "POST", "/settings", payload, &result); err != nil {
		apiError(c, http.StatusBadGateway, "无法保存备份配置，请刷新后检查实际配置")
		return
	}
	c.JSON(http.StatusOK, result)
}
