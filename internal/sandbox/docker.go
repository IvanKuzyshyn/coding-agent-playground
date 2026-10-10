// Package sandbox allocates and destroys sandboxes. Docker is the local provider; the interface
// is what a microVM/Kubernetes provider would implement in the real setup.
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"

	"github.com/google/uuid"

	"terrarium/internal/session"
)

var ErrNotFound = errors.New("sandbox not found")

const (
	LabelSessionID = "terrarium.session-id"
	labelManaged   = "terrarium.managed"
	sandboxdPort   = 7777
	apiVersion     = "v1.47"
)

type CreateRequest struct {
	SessionID    uuid.UUID
	Image        string
	Env          map[string]string
	Resources    session.Resources
	SandboxToken string
}

type Sandbox struct {
	SessionID   uuid.UUID
	ContainerID string
	Endpoint    string // sandboxd base URL reachable from the server
	Running     bool
}

type Provider interface {
	// Name returns the deterministic container name; recorded before Create as the cleanup obligation.
	Name(sessionID uuid.UUID) string
	// Create is idempotent by session id: an existing sandbox is returned (and started) as is.
	Create(ctx context.Context, req CreateRequest) (Sandbox, error)
	Get(ctx context.Context, sessionID uuid.UUID) (Sandbox, error)
	// Delete removes the sandbox and its home volume; deleting a missing sandbox is not an error.
	Delete(ctx context.Context, sessionID uuid.UUID) error
	List(ctx context.Context) ([]Sandbox, error)
}

type Docker struct {
	network string
	hc      *http.Client
}

// NewDocker talks to the Engine API on a unix socket. Sandboxes join only `network`.
func NewDocker(socketPath, network string) *Docker {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}}
	return &Docker{network: network, hc: &http.Client{Transport: tr}}
}

func (d *Docker) Name(id uuid.UUID) string { return "terrarium-sbx-" + id.String() }
func volumeName(id uuid.UUID) string      { return "terrarium-home-" + id.String() }

func (d *Docker) endpoint(id uuid.UUID) string {
	return fmt.Sprintf("http://%s:%d", d.Name(id), sandboxdPort)
}

func (d *Docker) Create(ctx context.Context, req CreateRequest) (Sandbox, error) {
	env := make([]string, 0, len(req.Env)+1)
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}
	env = append(env, "SANDBOXD_TOKEN="+req.SandboxToken)
	sort.Strings(env)

	body := map[string]any{
		"Image":  req.Image,
		"Env":    env,
		"Labels": map[string]string{LabelSessionID: req.SessionID.String(), labelManaged: "true"},
		"HostConfig": map[string]any{
			"NetworkMode":    d.network,
			"ReadonlyRootfs": true,
			"Tmpfs":          map[string]string{"/tmp": "rw,nosuid,nodev,size=512m"},
			"Mounts": []map[string]any{{
				"Type": "volume", "Source": volumeName(req.SessionID), "Target": "/home/agent",
			}},
			"CapDrop":       []string{"ALL"},
			"SecurityOpt":   []string{"no-new-privileges:true"},
			"PidsLimit":     req.Resources.PIDs,
			"NanoCpus":      int64(req.Resources.CPUs * 1e9),
			"Memory":        req.Resources.MemoryMB * 1024 * 1024,
			"RestartPolicy": map[string]string{"Name": "no"},
		},
	}
	q := url.Values{"name": {d.Name(req.SessionID)}}
	status, err := d.do(ctx, http.MethodPost, "/containers/create?"+q.Encode(), body, nil)
	if err != nil && status != http.StatusConflict {
		return Sandbox{}, fmt.Errorf("create container: %w", err)
	}
	// 409: a previous attempt already created it; start and return the same container.
	if status, err := d.do(ctx, http.MethodPost, "/containers/"+d.Name(req.SessionID)+"/start", nil, nil); err != nil && status != http.StatusNotModified {
		return Sandbox{}, fmt.Errorf("start container: %w", err)
	}
	return d.Get(ctx, req.SessionID)
}

func (d *Docker) Get(ctx context.Context, id uuid.UUID) (Sandbox, error) {
	var info struct {
		ID    string `json:"Id"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	status, err := d.do(ctx, http.MethodGet, "/containers/"+d.Name(id)+"/json", nil, &info)
	if status == http.StatusNotFound {
		return Sandbox{}, ErrNotFound
	}
	if err != nil {
		return Sandbox{}, err
	}
	return Sandbox{SessionID: id, ContainerID: info.ID, Endpoint: d.endpoint(id), Running: info.State.Running}, nil
}

func (d *Docker) Delete(ctx context.Context, id uuid.UUID) error {
	status, err := d.do(ctx, http.MethodDelete, "/containers/"+d.Name(id)+"?force=true", nil, nil)
	if err != nil && status != http.StatusNotFound {
		return fmt.Errorf("remove container: %w", err)
	}
	status, err = d.do(ctx, http.MethodDelete, "/volumes/"+volumeName(id)+"?force=true", nil, nil)
	if err != nil && status != http.StatusNotFound {
		return fmt.Errorf("remove volume: %w", err)
	}
	return nil
}

func (d *Docker) List(ctx context.Context) ([]Sandbox, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {labelManaged + "=true"}})
	var list []struct {
		ID     string            `json:"Id"`
		State  string            `json:"State"`
		Labels map[string]string `json:"Labels"`
	}
	if _, err := d.do(ctx, http.MethodGet, "/containers/json?all=true&filters="+url.QueryEscape(string(filters)), nil, &list); err != nil {
		return nil, err
	}
	out := make([]Sandbox, 0, len(list))
	for _, c := range list {
		id, err := uuid.Parse(c.Labels[LabelSessionID])
		if err != nil {
			continue
		}
		out = append(out, Sandbox{SessionID: id, ContainerID: c.ID, Endpoint: d.endpoint(id), Running: c.State == "running"})
	}
	return out, nil
}

// do returns the HTTP status even on error so callers can treat 404/409/304 as idempotent outcomes.
func (d *Docker) do(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker/"+apiVersion+path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, fmt.Errorf("docker %s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(msg))
	}
	if out != nil {
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}
