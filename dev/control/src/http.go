package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Configure Gin's JSON decoder once at startup; never change it during a request.
func init() { gin.EnableJsonDecoderDisallowUnknownFields() }

func apiError(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": message})
}

func bindJSON(c *gin.Context, target any, maxBytes int64, message string) bool {
	// Gin has no JSON body-size option; retain the net/http bounded reader.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
	if err := c.ShouldBindJSON(target); err != nil {
		apiError(c, http.StatusBadRequest, message)
		return false
	}
	return true
}

// handler registers native Gin routes while keeping the loopback-only access policy.
func (s *server) handler() *gin.Engine {
	router := gin.New()
	router.SetHTMLTemplate(template.Must(template.ParseFS(pages, "index.html", "debug.html", "backups.html", "logic_instances.html", "logic_backups.html", "refresh.html")))
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	// The controller is reached directly through SSH, not through a trusted HTTP proxy.
	_ = router.SetTrustedProxies(nil)
	router.Use(gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		log.Print("HTTP handler panic")
		apiError(c, http.StatusInternalServerError, "internal server error")
	}), controlAccess)
	router.NoRoute(func(c *gin.Context) { apiError(c, http.StatusNotFound, "not found") })
	router.GET("/api/debug/actions", func(c *gin.Context) { c.JSON(http.StatusOK, debugActions) })
	router.POST("/api/debug/:action", s.debugOperation)
	router.GET("/debug", func(c *gin.Context) { c.HTML(http.StatusOK, "debug.html", nil) })
	router.GET("/backups", func(c *gin.Context) { c.HTML(http.StatusOK, "backups.html", nil) })
	router.GET("/api/backups", s.listBackupServices)
	router.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	router.GET("/", func(c *gin.Context) { c.HTML(http.StatusOK, "index.html", nil) })
	router.GET("/api/logics", s.listLogics)
	router.GET("/logics/:logic_id/instances", func(c *gin.Context) {
		id := c.Param("logic_id")
		if !workspacePattern.MatchString(id) {
			apiError(c, http.StatusBadRequest, "invalid Logic ID")
			return
		}
		c.HTML(http.StatusOK, "logic_instances.html", gin.H{"LogicID": id})
	})
	router.GET("/logics/:logic_id/backups", func(c *gin.Context) {
		id := c.Param("logic_id")
		if !workspacePattern.MatchString(id) {
			apiError(c, http.StatusBadRequest, "invalid Logic ID")
			return
		}
		c.HTML(http.StatusOK, "logic_backups.html", gin.H{"LogicID": id})
	})
	router.GET("/api/jobs", s.listJobs)
	router.POST("/api/switch", s.switchRuntime)
	router.POST("/api/restore", s.restoreRuntime)
	router.POST("/api/instances/extend", s.extendInstance)
	router.POST("/api/instances/schedule", s.configureSchedule)
	return router
}

func controlAccess(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	r := c.Request
	host, _, _ := strings.Cut(r.Host, ":")
	if host != "127.0.0.1" && host != "localhost" {
		apiError(c, http.StatusForbidden, "loopback host required")
		return
	}
	if r.Method == http.MethodPost {
		origin, err := url.Parse(c.GetHeader("Origin"))
		if err != nil || (origin.Host != r.Host && !(c.GetHeader("Origin") == "" && c.GetHeader("X-Control-Request") == "1")) {
			apiError(c, http.StatusForbidden, "same-origin request required")
			return
		}
	}
	c.Next()
}

func (s *server) listLogics(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	inventory, err := s.inventory(ctx)
	if err != nil {
		apiError(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, inventory)
}

func (s *server) listJobs(c *gin.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.JSON(http.StatusOK, s.jobs)
}

func (s *server) restoreRuntime(c *gin.Context) {
	var req struct {
		LogicID string `json:"logic_id" binding:"required"`
	}
	if !bindJSON(c, &req, 4096, "invalid request") {
		return
	}
	s.mu.Lock()
	if s.busy {
		s.mu.Unlock()
		apiError(c, http.StatusConflict, "another operation is running")
		return
	}
	s.busy = true
	s.mu.Unlock()
	release := true
	defer func() {
		if release {
			s.mu.Lock()
			s.busy = false
			s.mu.Unlock()
		}
	}()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	v, e := s.inventory(ctx)
	if e != nil {
		apiError(c, http.StatusBadGateway, e.Error())
		return
	}
	var row *logic
	for i := range v.Logics {
		if v.Logics[i].ID == req.LogicID || v.Logics[i].OriginNode+"/"+v.Logics[i].OriginID == req.LogicID {
			row = &v.Logics[i]
			break
		}
	}
	if row == nil || len(row.Images) == 0 {
		apiError(c, http.StatusConflict, "Logic ID has no complete backup")
		return
	}
	if row.Owner == nil {
		apiError(c, http.StatusConflict, "backup ownership is not assigned; switch first")
		return
	}
	var rt runtimeConfig
	for _, candidate := range s.cfg.Runtimes {
		if candidate.Name == row.Owner.Node {
			rt = candidate
		}
	}
	if rt.Name == "" || v.Errors[rt.Name] != "" {
		apiError(c, http.StatusConflict, "owner Runtime unavailable")
		return
	}
	for _, instance := range row.Instances {
		if instance.Runtime == rt.Name {
			apiError(c, http.StatusConflict, "Logic ID already exists on target Runtime: "+instance.ID)
			return
		}
	}
	var random [16]byte
	rand.Read(random[:])
	j := job{ID: hex.EncodeToString(random[:]), LogicID: row.ID, Target: rt.Name, Archive: row.Images[0].Archive, Status: "queued", Created: time.Now().UTC().Format(time.RFC3339)}
	s.mu.Lock()
	s.jobs = append([]job{j}, s.jobs...)
	e = s.persist()
	s.mu.Unlock()
	if e != nil {
		apiError(c, http.StatusInternalServerError, "unable to persist operation")
		return
	}
	release = false
	go s.run(j, rt, row.Images[0])
	c.JSON(http.StatusAccepted, j)
}
