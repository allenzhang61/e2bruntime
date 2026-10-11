package main

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
)

// Extend from the later of now and the existing deadline, never shorten it.
func (s *server) extendInstance(c *gin.Context) {
	var req struct {
		Runtime string `json:"runtime" binding:"required"`
		ID      string `json:"sandbox_id" binding:"required"`
		Delay   int    `json:"delay_seconds" binding:"required,min=60,max=3600"`
	}
	if !bindJSON(c, &req, 4096, "invalid request (delay must be 60–3600 seconds)") {
		return
	}
	if !debugIDPattern.MatchString(req.ID) {
		apiError(c, 400, "valid sandbox_id required")
		return
	}
	var rt runtimeConfig
	for _, candidate := range s.cfg.Runtimes {
		if candidate.Name == req.Runtime {
			rt = candidate
		}
	}
	if rt.Name == "" {
		apiError(c, 400, "unknown Runtime")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	path := "/sandboxes/" + url.PathEscape(req.ID)
	var info struct {
		ID        string    `json:"sandboxID"`
		State     string    `json:"state"`
		End       time.Time `json:"endAt"`
		Lifecycle struct {
			OnTimeout string `json:"onTimeout"`
		} `json:"lifecycle"`
	}
	if _, err := s.call(ctx, rt, false, "GET", path, nil, &info); err != nil {
		apiError(c, 502, err.Error())
		return
	}
	if info.ID != req.ID || info.State != "running" || info.End.IsZero() {
		apiError(c, 409, "only a running instance with a known deadline can be extended")
		return
	}
	now := time.Now()
	base := info.End
	if base.Before(now) {
		base = now
	}
	deadline := base.Add(time.Duration(req.Delay) * time.Second)
	timeout := int(math.Ceil(deadline.Sub(now).Seconds()))
	if _, err := s.call(ctx, rt, false, "POST", path+"/timeout", gin.H{"timeout": timeout}, nil); err != nil {
		apiError(c, 502, err.Error())
		return
	}
	// Return the Runtime's observed deadline, not just a locally calculated value.
	if _, err := s.call(ctx, rt, false, "GET", path, nil, &info); err != nil {
		apiError(c, 502, "timeout updated but unable to verify instance; refresh before retry")
		return
	}
	c.JSON(http.StatusOK, gin.H{"runtime": rt.Name, "sandbox_id": req.ID, "end_at": info.End, "on_timeout": info.Lifecycle.OnTimeout, "delay_seconds": req.Delay})
}
