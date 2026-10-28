package envd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	api "github.com/catalystcommunity/magi/primitives/execution/api"
)

// Client calls one envd. HTTP carries the connection, so a backend can
// route it through a port-forward, a VM socket, or a direct address.
type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

func (c *Client) call(ctx context.Context, path string, req, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.Base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+c.Token)
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	res, err := hc.Do(r)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		e := &Error{}
		if json.Unmarshal(b, e) != nil || e.Code == 0 {
			e = errorf(res.StatusCode, "%s", strings.TrimSpace(string(b)))
		}
		return e
	}
	if err := json.Unmarshal(b, resp); err != nil {
		return fmt.Errorf("envd response: %w", err)
	}
	return nil
}

// Ping checks that envd answers and accepts the token.
func (c *Client) Ping(ctx context.Context) error {
	var out map[string]string
	return c.call(ctx, PathPing, struct{}{}, &out)
}

func (c *Client) Exec(ctx context.Context, req api.ExecRequest) (out api.ExecResult, err error) {
	err = c.call(ctx, PathExec, req, &out)
	return
}

func (c *Client) ReadFile(ctx context.Context, req api.ReadFileRequest) (out api.FileContent, err error) {
	err = c.call(ctx, PathRead, req, &out)
	return
}

func (c *Client) WriteFile(ctx context.Context, req api.WriteFileRequest) (out api.WriteFileResponse, err error) {
	err = c.call(ctx, PathWrite, req, &out)
	return
}

func (c *Client) ListFiles(ctx context.Context, req api.ListFilesRequest) (out api.ListFilesResponse, err error) {
	err = c.call(ctx, PathList, req, &out)
	return
}
