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
