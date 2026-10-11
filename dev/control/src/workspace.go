package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

const workspaceRegistryKey = "control/workspaces.json"

var workspacePattern = regexp.MustCompile(`^workspace-[a-z0-9]{6}$`)

type workspaceRegistry struct {
	Schema int               `json:"schema_version"`
	IDs    map[string]string `json:"ids"`
}

func newWorkspaceID() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	suffix := make([]byte, 6)
	for i := range suffix {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		suffix[i] = alphabet[n.Int64()]
	}
	return "workspace-" + string(suffix), nil
}

func s3Code(err error, codes ...string) bool {
	var api smithy.APIError
	if !errors.As(err, &api) {
		return false
	}
	for _, code := range codes {
		if api.ErrorCode() == code {
			return true
		}
	}
	return false
}

// The S3 registry uses conditional writes so concurrent inventories cannot assign
// two names to one origin or reuse the same six-character suffix for two origins.
func (s *server) workspaceIDs(ctx context.Context, rows map[string]*logic) (map[string]string, error) {
	if len(rows) == 0 {
		return map[string]string{}, nil
	}
	for attempt := 0; attempt < 8; attempt++ {
		registry := workspaceRegistry{Schema: 1, IDs: map[string]string{}}
		etag := ""
		obj, err := s.s3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(workspaceRegistryKey)})
		if err == nil {
			raw, readErr := io.ReadAll(io.LimitReader(obj.Body, (1<<20)+1))
			obj.Body.Close()
			if readErr != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &registry) != nil || registry.Schema != 1 || registry.IDs == nil {
				return nil, fmt.Errorf("invalid workspace registry")
			}
			etag = aws.ToString(obj.ETag)
			if etag == "" {
				return nil, fmt.Errorf("workspace registry ETag missing")
			}
		} else if !s3Code(err, "NoSuchKey", "NotFound", "404") {
			return nil, fmt.Errorf("workspace registry unavailable")
		}
		used := map[string]bool{}
		for _, name := range registry.IDs {
			if !workspacePattern.MatchString(name) || used[name] {
				return nil, fmt.Errorf("invalid or duplicate workspace name")
			}
			used[name] = true
		}
		changed := false
		for origin := range rows {
			if registry.IDs[origin] != "" {
				continue
			}
			var name string
			for {
				name, err = newWorkspaceID()
				if err != nil {
					return nil, fmt.Errorf("workspace randomness unavailable")
				}
				if !used[name] {
					break
				}
			}
			registry.IDs[origin] = name
			used[name] = true
			changed = true
		}
		if !changed {
			return registry.IDs, nil
		}
		raw, _ := json.Marshal(registry)
		if len(raw) > 1<<20 {
			return nil, fmt.Errorf("workspace registry size limit reached")
		}
		input := &s3.PutObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(workspaceRegistryKey), Body: bytes.NewReader(raw), ContentType: aws.String("application/json")}
		if etag == "" {
			input.IfNoneMatch = aws.String("*")
		} else {
			input.IfMatch = aws.String(etag)
		}
		if _, err = s.s3.PutObject(ctx, input); err == nil {
			return registry.IDs, nil
		}
		if !s3Code(err, "PreconditionFailed", "ConditionalRequestConflict") {
			return nil, fmt.Errorf("workspace registry update failed")
		}
	}
	return nil, fmt.Errorf("workspace registry busy; retry")
}
