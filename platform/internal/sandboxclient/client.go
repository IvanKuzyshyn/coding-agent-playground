// Package sandboxclient is the supervisor's client for sandboxd, the exec daemon inside each
// sandbox container. See docs/contracts/sandboxd.md.
package sandboxclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrExited is returned by Signal when the command has already exited.
	ErrExited = errors.New("command exited")
)

// Command states.
const (
	StateRunning = "running"
	StateExited  = "exited"
)

type CommandRequest struct {
	ID   string            `json:"id"`
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env,omitempty"`
	Cwd  string            `json:"cwd,omitempty"`
}

type Command struct {
	ID        string     `json:"id"`
	State     string     `json:"state"` // "running" | "exited"
	ExitCode  *int       `json:"exit_code"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
}

type Client struct {
	baseURL string
	token   string
	hc      *http.Client
}

// New returns a client for the sandboxd at baseURL (e.g. "http://10.0.0.5:7777").
// A nil hc gets a default client with a request timeout.
func New(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, hc: hc}
}

func (c *Client) Health(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/healthz", nil, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return expect(resp, http.StatusOK)
}

func (c *Client) PutFile(ctx context.Context, path string, data []byte, mode os.FileMode) error {
	q := url.Values{"path": {path}, "mode": {fmt.Sprintf("%04o", mode.Perm())}}
	resp, err := c.do(ctx, http.MethodPut, "/files", q, bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return expect(resp, http.StatusNoContent)
}

func (c *Client) GetFile(ctx context.Context, path string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, "/files", url.Values{"path": {path}}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return nil, err
	}
	return io.ReadAll(resp.Body)
}

// StartCommand starts req, or returns the existing command's status if req.ID is already known.
func (c *Client) StartCommand(ctx context.Context, req CommandRequest) (Command, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Command{}, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/commands", nil, bytes.NewReader(body))
	if err != nil {
		return Command{}, err
	}
	defer resp.Body.Close()
	return decodeCommand(resp)
}

func (c *Client) Command(ctx context.Context, id string) (Command, error) {
	resp, err := c.do(ctx, http.MethodGet, "/commands/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return Command{}, err
	}
	defer resp.Body.Close()
	return decodeCommand(resp)
}

// Logs returns up to 1 MiB of stream ("stdout" or "stderr") from offset, the offset to read from
// next, and the command state observed before reading. State "exited" with empty data means the
// stream has been read completely.
func (c *Client) Logs(ctx context.Context, id, stream string, offset int64) (data []byte, next int64, state string, err error) {
	q := url.Values{"stream": {stream}, "offset": {strconv.FormatInt(offset, 10)}}
	resp, err := c.do(ctx, http.MethodGet, "/commands/"+url.PathEscape(id)+"/logs", q, nil)
	if err != nil {
		return nil, 0, "", err
	}
	defer resp.Body.Close()
	if err := expect(resp, http.StatusOK); err != nil {
		return nil, 0, "", err
	}
	data, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, "", err
	}
	next, err = strconv.ParseInt(resp.Header.Get("X-Next-Offset"), 10, 64)
	if err != nil {
		return nil, 0, "", fmt.Errorf("sandboxd: bad X-Next-Offset: %w", err)
	}
	return data, next, resp.Header.Get("X-Command-State"), nil
}

// Signal sends "TERM" or "KILL" to the command's process group. ErrExited if it already exited.
func (c *Client) Signal(ctx context.Context, id, signal string) error {
	body, err := json.Marshal(map[string]string{"signal": signal})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, "/commands/"+url.PathEscape(id)+"/signal", nil, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return ErrExited
	}
	return expect(resp, http.StatusNoContent)
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body io.Reader) (*http.Response, error) {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sandboxd %s %s: %w", method, path, err)
	}
	return resp, nil
}

func expect(resp *http.Response, status int) error {
	if resp.StatusCode == status {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("sandboxd %s %s: status %d: %s",
		resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, strings.TrimSpace(string(msg)))
}

func decodeCommand(resp *http.Response) (Command, error) {
	if err := expect(resp, http.StatusOK); err != nil {
		return Command{}, err
	}
	var cmd Command
	if err := json.NewDecoder(resp.Body).Decode(&cmd); err != nil {
		return Command{}, fmt.Errorf("sandboxd: decode command: %w", err)
	}
	return cmd, nil
}
