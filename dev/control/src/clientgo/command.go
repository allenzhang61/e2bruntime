package clientgo

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type CommandResult struct {
	Stdout, Stderr string
	ExitCode       int
}

func (c *Client) RunCommand(ctx context.Context, sb Sandbox, command string) (CommandResult, error) {
	var result CommandResult
	body, err := json.Marshal(map[string]any{"process": map[string]any{"cmd": "/bin/bash", "args": []string{"-l", "-c", command}}, "stdin": false})
	if err != nil {
		return result, err
	}
	frame := make([]byte, 5+len(body))
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(body)))
	copy(frame[5:], body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ProxyURL+"/process.Process/Start", bytes.NewReader(frame))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/connect+json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("E2b-Sandbox-Id", sb.ID)
	req.Header.Set("E2b-Sandbox-Port", "49983")
	req.Header.Set("X-Access-Token", sb.AccessToken)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return result, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return result, fmt.Errorf("command HTTP %d", res.StatusCode)
	}
	result, err = readCommandStream(res.Body)
	if err != nil {
		message := strings.ReplaceAll(err.Error(), sb.AccessToken, "[REDACTED]")
		message = strings.ReplaceAll(message, c.APIKey, "[REDACTED]")
		return result, errors.New(message)
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("command exit code %d", result.ExitCode)
	}
	return result, nil
}
func readCommandStream(reader io.Reader) (result CommandResult, err error) {
	const maxStream = 4 << 20
	total := 0
	exited := false
	for {
		var header [5]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return result, fmt.Errorf("command stream ended before final envelope: %w", err)
		}
		length := int(binary.BigEndian.Uint32(header[1:]))
		total += 5 + length
		if total > maxStream {
			return result, errors.New("command stream exceeds 4 MiB")
		}
		payload := make([]byte, length)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return result, err
		}
		switch header[0] {
		case 2: // Connect end-stream envelope, distinct from the process exit event.
			var trailer struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(payload, &trailer); err != nil {
				return result, err
			}
			if trailer.Error != nil {
				return result, fmt.Errorf("RPC %s: %s", trailer.Error.Code, trailer.Error.Message)
			}
			if !exited {
				return result, errors.New("command stream has no process exit event")
			}
			return result, nil
		case 0:
			var message struct {
				Event struct {
					Data *struct {
						Stdout []byte `json:"stdout"`
						Stderr []byte `json:"stderr"`
					} `json:"data"`
					End *struct {
						ExitCode int    `json:"exitCode"`
						Exited   bool   `json:"exited"`
						Error    string `json:"error"`
					} `json:"end"`
				} `json:"event"`
			}
			if err := json.Unmarshal(payload, &message); err != nil {
				return result, err
			}
			if message.Event.Data != nil {
				result.Stdout += string(message.Event.Data.Stdout)
				result.Stderr += string(message.Event.Data.Stderr)
			}
			if end := message.Event.End; end != nil {
				result.ExitCode = end.ExitCode
				if end.Error != "" {
					return result, errors.New(end.Error)
				}
				if !end.Exited {
					return result, errors.New("process ended without normal exit")
				}
				exited = true
			}
		default:
			return result, errors.New("unsupported command stream envelope flags")
		}
	}
}
