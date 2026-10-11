package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSharedLayerReusedAcrossWorkspaces(t *testing.T) {
	reads := 0
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" || r.URL.Path != "/test/"+sharedLayerPrefix("runtime1")+baseID+".tar.gz" {
			t.Errorf("unexpected shared layer lookup: %s %s", r.Method, r.URL.Path)
		}
		reads++
		sum := sha256.Sum256([]byte("shared"))
		w.Header().Set("Content-Length", fmt.Sprint(6))
		w.Header().Set("X-Amz-Meta-Sha256", hex.EncodeToString(sum[:]))
	}))
	defer node.Close()
	client := s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}, func(o *s3.Options) { o.BaseEndpoint = aws.String(node.URL); o.UsePathStyle = true })
	w := worker{cfg: config{Node: "runtime1", Bucket: "test"}, s3: client}
	for _, logic := range []string{"workspace-abc123", "workspace-def456"} {
		ctx := context.WithValue(context.Background(), workspaceContextKey{}, logic)
		key, err := w.ensureLayer(ctx, baseID)
		if err != nil || key != sharedLayerPrefix("runtime1")+baseID+".tar.gz" {
			t.Fatalf("workspace-specific shared layer: %s %v", key, err)
		}
	}
	if reads != 2 {
		t.Fatal("layer not checked before reuse")
	}
}
