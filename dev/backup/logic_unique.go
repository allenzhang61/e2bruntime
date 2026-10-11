package main

import (
	"context"
	"fmt"
	"net/url"
)

// Called while holding the node flock: all supported imports share this lock.
// A paused sandbox reserves its logical identity just like a running sandbox.
func (w *worker) requireLogicalVacancy(ctx context.Context, node, id string) error {
	for _, state := range []string{"running", "paused"} {
		token := ""
		seen := map[string]bool{}
		for {
			path := "/v2/sandboxes?state=" + state + "&limit=100"
			if token != "" {
				path += "&nextToken=" + url.QueryEscape(token)
			}
			var page []sandbox
			next, err := w.api.call(ctx, "GET", path, nil, &page)
			if err != nil {
				return fmt.Errorf("target instance inventory unavailable; import denied")
			}
			for _, sb := range page {
				n, s, err := logicalIdentity(w.cfg.Node, sb.ID, sb.Metadata)
				if err != nil {
					return err
				}
				if n == node && s == id {
					return fmt.Errorf("Logic ID already exists on this Runtime: instance %s (%s)", sb.ID, state)
				}
			}
			if next == "" {
				break
			}
			if seen[next] {
				return fmt.Errorf("target inventory pagination loop")
			}
			seen[next] = true
			token = next
		}
	}
	return nil
}

// Explicit controller intent starts a new generation after an instance expires.
func (w *worker) prepareLogicalImport(ctx context.Context, e transferEntry) error {
	return w.withNodeLock(ctx, func() error {
		if err := w.validateTransfer(e); err != nil {
			return err
		}
		m, err := w.readRecovery(ctx, e.ArchiveKey)
		if err != nil {
			return err
		}
		n, id, err := logicalIdentity(e.SourceNode, e.SandboxID, m.Sandbox.Metadata)
		if err != nil {
			return err
		}
		if err = w.requireLogicalVacancy(ctx, n, id); err != nil {
			return err
		}
		old, etag, err := w.readOwner(ctx, n, id)
		if err != nil {
			return fmt.Errorf("ownership record unavailable")
		}
		if old.OwnerNode != w.cfg.Node {
			return fmt.Errorf("backup is assigned to another Runtime; switch ownership before restoring")
		}
		if old.OwnerNode == w.cfg.Node && old.State == "importing" {
			return fmt.Errorf("previous import incomplete; inspect target before retry")
		}
		if old.OwnerNode == w.cfg.Node && old.State == "ready" && old.PinnedArchive == e.ArchiveKey {
			return nil
		}
		old.OwnerNode = w.cfg.Node
		old.OwnerSandboxID = ""
		old.State = "ready"
		old.PinnedArchive = e.ArchiveKey
		old.Generation++
		_, err = w.writeOwner(ctx, old, etag)
		return err
	})
}
