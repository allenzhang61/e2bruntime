package main

import (
	"context"
	"fmt"
	"net/url"
	"time"
)

// Reconcile observed completion after a controller restart; never replay create.
func (s *server) reconcile(j job) {
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	for {
		if id, err := s.recoveredInstance(ctx, j); err == nil {
			s.update(j.ID, "succeeded", id, nil)
			return
		}
		select {
		case <-ctx.Done():
			s.update(j.ID, "failed", "", fmt.Errorf("unable to verify interrupted restore; inspect target before retry"))
			return
		case <-time.After(3 * time.Second):
		}
	}
}
func (s *server) recoveredInstance(ctx context.Context, j job) (string, error) {
	v, err := s.inventory(ctx)
	if err != nil {
		return "", err
	}
	for _, row := range v.Logics {
		if (row.ID != j.LogicID && row.OriginNode+"/"+row.OriginID != j.LogicID) || row.Owner == nil || row.Owner.Node != j.Target || row.Owner.State != "active" || row.Owner.Archive != j.Archive || row.CurrentState != "running" && row.CurrentState != "paused" {
			continue
		}
		var rt runtimeConfig
		for _, candidate := range s.cfg.Runtimes {
			if candidate.Name == j.Target {
				rt = candidate
			}
		}
		var info struct {
			ID       string            `json:"sandboxID"`
			Started  string            `json:"startedAt"`
			Metadata map[string]string `json:"metadata"`
		}
		if _, err = s.call(ctx, rt, false, "GET", "/sandboxes/"+url.PathEscape(row.Owner.Instance), nil, &info); err != nil {
			return "", err
		}
		n, id, err := identity(rt.Name, info.ID, info.Metadata)
		if err != nil || (n != row.OriginNode || id != row.OriginID) || info.ID != row.Owner.Instance {
			return "", fmt.Errorf("restored identity mismatch")
		}
		started, e1 := time.Parse(time.RFC3339Nano, info.Started)
		created, e2 := time.Parse(time.RFC3339Nano, j.Created)
		if e1 != nil || e2 != nil || started.Before(created) {
			return "", fmt.Errorf("instance predates operation")
		}
		return info.ID, nil
	}
	return "", fmt.Errorf("restored target not yet observed")
}
