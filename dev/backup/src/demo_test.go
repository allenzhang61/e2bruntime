package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDemoRunningLive(t *testing.T) {
	if os.Getenv("BACKUP_DEMO_LIVE") != "1" {
		t.Skip("set BACKUP_DEMO_LIVE=1")
	}
	c, e := configInit()
	if e != nil {
		t.Fatal(e)
	}
	a := newAPI(c)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sbs, e := a.running(ctx)
	if e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, sb := range sbs {
		if sb.Metadata["purpose"] != "periodic-backup-demo" {
			continue
		}
		count++
		var current sandbox
		_, e = a.call(ctx, "GET", "/sandboxes/"+sb.ID, nil, &current)
		if e != nil {
			t.Fatal(e)
		}
		current.ID = sb.ID
		result, e := a.RunCommand(ctx, current, "tail -n 1 /home/user/backup-demo.log")
		if e != nil {
			t.Fatal(e)
		}
		stamp, e := time.Parse(time.RFC3339, strings.TrimSpace(result.Stdout))
		if e != nil {
			t.Fatal("missing demo timestamp")
		}
		if age := time.Since(stamp); age < -time.Minute || age > time.Minute {
			t.Fatal("demo process stopped writing")
		}
		t.Logf("node=%s sandbox=%s running, latest file timestamp=%s", c.Node, sb.ID, stamp.Format(time.RFC3339))
	}
	if count != 1 {
		t.Fatalf("expected one demo, got %d", count)
	}
}
