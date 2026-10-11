package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestInventoryReadsActualTimeoutPolicy(t *testing.T) {
	for _, policy := range []string{"pause", "kill", "unavailable"} {
		t.Run(policy, func(t *testing.T) {
			store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Has("list-type") {
					fmt.Fprint(w, `<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`)
					return
				}
				if strings.HasSuffix(r.URL.Path, "workspaces.json") {
					w.Header().Set("ETag", `"registry"`)
					fmt.Fprint(w, `{"schema_version":1,"ids":{"runtime1/instance":"workspace-abc123"}}`)
					return
				}
				w.WriteHeader(404)
				fmt.Fprint(w, `<Error><Code>NoSuchKey</Code></Error>`)
			}))
			defer store.Close()
			rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/sandboxes" {
					fmt.Fprint(w, `[{"sandboxID":"instance","state":"running","metadata":{}}]`)
					return
				}
				if policy == "unavailable" {
					w.WriteHeader(503)
					return
				}
				fmt.Fprintf(w, `{"sandboxID":"instance","state":"running","endAt":"2026-10-11T10:00:00Z","lifecycle":{"onTimeout":%q}}`, policy)
			}))
			defer rt.Close()
			s := server{cfg: config{Bucket: "test", Runtimes: []runtimeConfig{{Name: "runtime1", API: rt.URL}}}, http: http.DefaultClient, s3: s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(store.URL); o.UsePathStyle = true })}
			v, err := s.inventory(context.Background())
			if err != nil || len(v.Logics) != 1 || len(v.Logics[0].Instances) != 1 {
				t.Fatalf("inventory failed: %v %+v", err, v)
			}
			i := v.Logics[0].Instances[0]
			if policy == "unavailable" {
				if i.EndAt != "" || i.OnTimeout != "" {
					t.Fatal("invented a deadline or policy")
				}
			} else if i.EndAt != "2026-10-11T10:00:00Z" || i.OnTimeout != policy {
				t.Fatalf("wrong actual deadline: %+v", i)
			}
		})
	}
}
