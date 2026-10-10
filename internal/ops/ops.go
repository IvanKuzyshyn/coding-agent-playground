// Package ops holds the side-effecting session operations. Each one takes the frozen spec plus
// stable ids and is safe to repeat after a crash; these are the future Temporal activities
// (plan §4.4). Nothing here knows what drives them.
package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"terrarium/internal/credentials"
	"terrarium/internal/harness"
	"terrarium/internal/sandbox"
	"terrarium/internal/sandboxclient"
	"terrarium/internal/session"
	"terrarium/internal/store"
)

// SandboxAPI is the subset of the sandboxd client the operations use.
type SandboxAPI interface {
	Health(ctx context.Context) error
	PutFile(ctx context.Context, path string, data []byte, mode fs.FileMode) error
	StartCommand(ctx context.Context, req sandboxclient.CommandRequest) (sandboxclient.Command, error)
	Command(ctx context.Context, id string) (sandboxclient.Command, error)
	Logs(ctx context.Context, id, stream string, offset int64) ([]byte, int64, string, error)
	Signal(ctx context.Context, id, signal string) error
}

type Ops struct {
	Store      *store.Store
	Sandboxes  sandbox.Provider
	NewClient  func(endpoint, token string) SandboxAPI
	Gateway    harness.Gateway
	SessionKey []byte
	Log        *slog.Logger

	// PollInterval is how often a running turn's logs are read when there is no new output.
	PollInterval time.Duration
	// StopGrace is how long a TERM'd harness gets before KILL.
	StopGrace time.Duration
}

// ErrLost means the sandbox disappeared under a live session.
var ErrLost = errors.New("sandbox lost")

func (o *Ops) secret(id uuid.UUID) string {
	return credentials.DeriveSecret(o.SessionKey, id.String())
}

func (o *Ops) client(s store.Session) SandboxAPI {
	return o.NewClient(fmt.Sprintf("http://%s:7777", o.Sandboxes.Name(s.ID)), s.SandboxToken)
}

// Provision creates the sandbox, waits for sandboxd, writes harness config and clones the repo.
// Every step is idempotent: the container is found by name, files are overwritten, and the
// clone command is keyed by a stable id.
func (o *Ops) Provision(ctx context.Context, s store.Session) error {
	if _, err := o.Store.SetStatus(ctx, s.ID, []string{session.StatusPending}, session.StatusProvisioning, "creating sandbox"); err != nil {
		return err
	}
	// Cleanup obligation before the resource exists.
	if err := o.Store.SetContainerID(ctx, s.ID, o.Sandboxes.Name(s.ID)); err != nil {
		return err
	}
	secret := o.secret(s.ID)
	if _, err := o.Sandboxes.Create(ctx, sandbox.CreateRequest{
		SessionID:    s.ID,
		Image:        s.Spec.Image,
		Env:          harness.ContainerEnv(s.Spec, o.Gateway, secret),
		Resources:    s.Spec.Resources,
		SandboxToken: s.SandboxToken,
	}); err != nil {
		return err
	}
	c := o.client(s)
	if err := waitHealthy(ctx, c, 60*time.Second); err != nil {
		return err
	}
	for _, f := range harness.SetupFiles(s.Spec, o.Gateway, secret, time.Now().UTC().Format(time.RFC3339)) {
		if err := c.PutFile(ctx, f.Path, f.Data, fs.FileMode(f.Mode)); err != nil {
			return fmt.Errorf("write %s: %w", f.Path, err)
		}
	}
	if s.Spec.Repo != "" {
		o.event(ctx, s.ID, store.EventSystem, nil, map[string]string{"message": "cloning " + s.Spec.Repo})
		code, stderr, err := o.runToCompletion(ctx, c, sandboxclient.CommandRequest{
			ID:   "provision-clone",
			Argv: []string{"git", "clone", "--depth", "1", "https://github.com/" + s.Spec.Repo + ".git", harness.WorkDir},
			Cwd:  harness.Home,
		})
		if err != nil {
			return err
		}
		if code != 0 {
			return permanent(fmt.Errorf("git clone exited %d: %s", code, lastLine(stderr)))
		}
	}
	return o.becomeIdle(ctx, s, []string{session.StatusProvisioning}, "sandbox ready")
}

func (o *Ops) becomeIdle(ctx context.Context, s store.Session, from []string, msg string) error {
	if s.Spec.Limits.IdleTimeoutSeconds > 0 {
		t := time.Now().Add(time.Duration(s.Spec.Limits.IdleTimeoutSeconds) * time.Second)
		if err := o.Store.SetIdleUntil(ctx, s.ID, &t); err != nil {
			return err
		}
	}
	_, err := o.Store.SetStatus(ctx, s.ID, from, session.StatusIdle, msg)
	return err
}

func waitHealthy(ctx context.Context, c SandboxAPI, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := c.Health(ctx)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sandboxd not healthy after %s: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// runToCompletion starts (or re-attaches to) a short command and returns its exit code and stderr.
func (o *Ops) runToCompletion(ctx context.Context, c SandboxAPI, req sandboxclient.CommandRequest) (int, string, error) {
	if _, err := c.StartCommand(ctx, req); err != nil {
		return 0, "", err
	}
	for {
		cmd, err := c.Command(ctx, req.ID)
		if err != nil {
			return 0, "", err
		}
		if cmd.State == "exited" {
			stderr, _, _, _ := c.Logs(ctx, req.ID, "stderr", 0)
			code := -1
			if cmd.ExitCode != nil {
				code = *cmd.ExitCode
			}
			return code, string(stderr), nil
		}
		select {
		case <-ctx.Done():
			return 0, "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Cleanup deletes the sandbox and ends the session. Safe to call repeatedly.
func (o *Ops) Cleanup(ctx context.Context, s store.Session, reason string) error {
	if err := o.Sandboxes.Delete(ctx, s.ID); err != nil {
		return err
	}
	if err := o.Store.CancelQueuedTurns(ctx, s.ID, session.TurnStopped, "session ended: "+reason); err != nil {
		return err
	}
	_, err := o.Store.SetStatus(ctx, s.ID, []string{session.StatusStopping, session.StatusFailed}, session.StatusStopped, reason)
	return err
}

// Permanent marks errors that retries cannot fix.
type permanentError struct{ error }

func permanent(err error) error { return permanentError{err} }

func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

func (o *Ops) event(ctx context.Context, id uuid.UUID, kind string, turn *int, payload any) {
	if err := o.Store.AppendEvent(ctx, id, kind, turn, payload); err != nil {
		o.Log.Warn("append event", "session", id, "err", err)
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// splitLines returns complete lines and how many bytes they consumed.
func splitLines(b []byte) (lines [][]byte, consumed int) {
	for {
		i := bytes.IndexByte(b[consumed:], '\n')
		if i < 0 {
			return lines, consumed
		}
		lines = append(lines, b[consumed:consumed+i])
		consumed += i + 1
	}
}

var errSandboxNotFound = sandbox.ErrNotFound
