package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"io"
	"os"
)

// Shared layers are immutable and are deliberately outside the per-sandbox
// retention prefix. Never delete them while an archive may reference them.
func (w *worker) ensureLayer(ctx context.Context, id string) (string, error) {
	key := sharedLayerPrefix(w.cfg.Node) + id + ".tar.gz"
	head, e := w.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(key)})
	if e == nil {
		if aws.ToInt64(head.ContentLength) <= 0 || head.Metadata["sha256"] == "" {
			return "", fmt.Errorf("shared layer metadata invalid")
		}
		return key, nil
	}
	var api smithy.APIError
	if !errors.As(e, &api) || (api.ErrorCode() != "NotFound" && api.ErrorCode() != "NoSuchKey" && api.ErrorCode() != "404") {
		return "", fmt.Errorf("shared layer HEAD failed (%T)", e)
	}
	f, e := os.CreateTemp("", "e2b-layer-*.tar.gz")
	if e != nil {
		return "", e
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	ex := Exporter{StorageRoot: w.cfg.Storage, AllowMemoryHeader: true, ExternalBuild: w.ensureLayer}
	if _, e = ex.ExportBuild(ctx, Record{BuildID: id, Kind: "filesystem"}, io.MultiWriter(f, hash)); e != nil {
		return "", e
	}
	st, e := f.Stat()
	if e != nil {
		return "", e
	}
	if _, e = f.Seek(0, 0); e != nil {
		return "", e
	}
	sha := hex.EncodeToString(hash.Sum(nil))
	_, e = w.s3.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(key), Body: f, Metadata: map[string]string{"sha256": sha, "build-id": id, "kind": "filesystem-base"}})
	if e != nil {
		return "", fmt.Errorf("shared layer PUT failed (%T)", e)
	}
	head, e = w.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(w.cfg.Bucket), Key: aws.String(key)})
	if e != nil || aws.ToInt64(head.ContentLength) != st.Size() || head.Metadata["sha256"] != sha {
		return "", fmt.Errorf("shared layer verification failed")
	}
	return key, nil
}

func sharedLayerPrefix(node string) string {
	return "backups/_shared/" + node + "/layers/"
}
