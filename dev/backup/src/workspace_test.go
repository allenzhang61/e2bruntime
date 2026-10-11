package main

import (
	"context"
	"testing"
)

func TestWorkspaceRegistryStableAcrossRuntime(t *testing.T) {
	s3 := ownerFixture(t)
	a := worker{cfg: config{Node: "runtime1", Bucket: "test"}, s3: s3}
	b := worker{cfg: config{Node: "runtime2", Bucket: "test"}, s3: s3}
	first, err := a.workspaceIDs(context.Background(), "runtime1/original")
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.workspaceIDs(context.Background(), "runtime1/original")
	if err != nil {
		t.Fatal(err)
	}
	if first["runtime1/original"] != second["runtime1/original"] || !workspacePattern.MatchString(first["runtime1/original"]) {
		t.Fatal("workspace changed across Runtime")
	}
	third, err := b.workspaceIDs(context.Background(), "runtime2/other")
	if err != nil {
		t.Fatal(err)
	}
	if third["runtime1/original"] == third["runtime2/other"] {
		t.Fatal("workspace collision")
	}
}

func TestArchiveIdentityWorkspaceLayout(t *testing.T) {
	key := "backups/workspace-abc123/runtime2/demo/filesystem/20261010T070000.000000000Z-" + childID + ".tar.gz"
	node, id, build, _, err := archiveIdentity(key)
	if err != nil || node != "runtime2" || id != "demo" || build != childID {
		t.Fatalf("unexpected identity: %s %s %s %v", node, id, build, err)
	}
	if _, _, _, _, err := archiveIdentity("backups/workspace-invalid/runtime2/demo/filesystem/20261010T070000.000000000Z-" + childID + ".tar.gz"); err == nil {
		t.Fatal("invalid workspace accepted")
	}
}
