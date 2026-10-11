package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestWorkspaceNamePersistsAndConcurrentAssignmentAgrees(t *testing.T) {
	var mu sync.Mutex
	var body []byte
	version := 0
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "GET" {
			if body == nil {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(404)
				io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)
				return
			}
			w.Header().Set("ETag", fmt.Sprintf(`"%d"`, version))
			w.Write(body)
			return
		}
		if r.Method == "PUT" {
			if (body != nil && r.Header.Get("If-None-Match") == "*") || (body != nil && r.Header.Get("If-Match") != fmt.Sprintf(`"%d"`, version)) {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(412)
				io.WriteString(w, `<Error><Code>PreconditionFailed</Code></Error>`)
				return
			}
			body, _ = io.ReadAll(r.Body)
			version++
			w.Header().Set("ETag", fmt.Sprintf(`"%d"`, version))
			return
		}
		t.Errorf("unexpected method %s", r.Method)
	}))
	defer store.Close()
	client := s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(store.URL); o.UsePathStyle = true })
	server1 := server{cfg: config{Bucket: "test"}, s3: client}
	origin := "runtime1/source"
	rows := map[string]*logic{origin: {OriginNode: "runtime1", OriginID: "source"}}
	type outcome struct {
		ids map[string]string
		err error
	}
	outcomes := make(chan outcome, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids, err := server1.workspaceIDs(context.Background(), rows)
			outcomes <- outcome{ids, err}
		}()
	}
	wg.Wait()
	close(outcomes)
	name := ""
	for result := range outcomes {
		if result.err != nil {
			t.Fatal(result.err)
		}
		got := result.ids[origin]
		if !workspacePattern.MatchString(got) {
			t.Fatalf("bad name %q", got)
		}
		if name != "" && name != got {
			t.Fatal("concurrent assignment produced different names")
		}
		name = got
	}
	// A new controller process, and a restored physical instance, retain the origin mapping.
	server2 := server{cfg: config{Bucket: "test"}, s3: client}
	rows["runtime2/other"] = &logic{OriginNode: "runtime2", OriginID: "other"}
	ids, err := server2.workspaceIDs(context.Background(), rows)
	if err != nil {
		t.Fatal(err)
	}
	if ids[origin] != name || ids["runtime2/other"] == name {
		t.Fatal("unstable or duplicate names")
	}
}
