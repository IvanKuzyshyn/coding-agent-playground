package testutil

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"terrarium/internal/sandbox"
	"terrarium/internal/sandboxclient"
)

// Fleet is an in-memory sandbox.Provider plus the sandboxd inside each sandbox. A sandboxd is
// reachable only while its sandbox exists and runs; deleting or losing the sandbox discards its
// commands, like removing the container would.
type Fleet struct {
	// Program decides what a newly started command does. Nil runs every command as "exit 0".
	Program func(sessionID uuid.UUID, req sandboxclient.CommandRequest) Program
	// CreateErr, if set, is consulted on every Create call (1-based); a non-nil error fails it.
	CreateErr func(call int) error
	// OnLogs runs before every Logs call, outside the fleet lock.
	OnLogs func(ctx context.Context, sessionID uuid.UUID, id, stream string, offset int64)

	mu      sync.Mutex
	boxes   map[uuid.UUID]*Box
	creates int
}

// Box is one fake sandbox and its sandboxd state.
type Box struct {
	Running bool
	Req     sandbox.CreateRequest
	Files   map[string][]byte
	cmds    map[string]*Cmd
}

// Cmd is one fake process.
type Cmd struct {
	Argv    []string
	Signals []string

	prog     Program
	pc       int
	stdout   []byte
	stderr   []byte
	exitCode *int
	started  time.Time
}

func NewFleet() *Fleet { return &Fleet{boxes: map[uuid.UUID]*Box{}} }

var errUnreachable = errors.New("sandboxd: connection refused")

// --- sandbox.Provider ---

func (f *Fleet) Name(id uuid.UUID) string { return "sbx-" + id.String() }

func (f *Fleet) Create(_ context.Context, req sandbox.CreateRequest) (sandbox.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if f.CreateErr != nil {
		if err := f.CreateErr(f.creates); err != nil {
			return sandbox.Sandbox{}, err
		}
	}
	b, ok := f.boxes[req.SessionID]
	if !ok {
		b = &Box{Req: req, Files: map[string][]byte{}, cmds: map[string]*Cmd{}}
		f.boxes[req.SessionID] = b
	}
	b.Running = true
	return f.view(req.SessionID, b), nil
}

func (f *Fleet) Get(_ context.Context, id uuid.UUID) (sandbox.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.boxes[id]
	if !ok {
		return sandbox.Sandbox{}, sandbox.ErrNotFound
	}
	return f.view(id, b), nil
}

func (f *Fleet) Delete(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.boxes, id)
	return nil
}

func (f *Fleet) List(context.Context) ([]sandbox.Sandbox, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sandbox.Sandbox, 0, len(f.boxes))
	for id, b := range f.boxes {
		out = append(out, f.view(id, b))
	}
	return out, nil
}

func (f *Fleet) view(id uuid.UUID, b *Box) sandbox.Sandbox {
	return sandbox.Sandbox{SessionID: id, ContainerID: "c-" + id.String(), Endpoint: f.endpoint(id), Running: b.Running}
}

func (f *Fleet) endpoint(id uuid.UUID) string { return fmt.Sprintf("http://%s:7777", f.Name(id)) }

// --- test controls ---

// Creates counts Create calls, including failed ones.
func (f *Fleet) Creates() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates
}

func (f *Fleet) Exists(id uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.boxes[id]
	return ok
}

func (f *Fleet) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.boxes)
}

// Lose makes the sandbox vanish, as if the container was removed behind the platform's back.
func (f *Fleet) Lose(id uuid.UUID) { f.Delete(context.Background(), id) } //nolint:errcheck

// AddOrphan creates a running sandbox without going through any session.
func (f *Fleet) AddOrphan(id uuid.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boxes[id] = &Box{Running: true, Files: map[string][]byte{}, cmds: map[string]*Cmd{}}
}

// Command returns a copy of the command's argv and signals, and whether it was started.
func (f *Fleet) Command(sessionID uuid.UUID, id string) (Cmd, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.boxes[sessionID]
	if !ok {
		return Cmd{}, false
	}
	c, ok := b.cmds[id]
	if !ok {
		return Cmd{}, false
	}
	return Cmd{Argv: append([]string(nil), c.Argv...), Signals: append([]string(nil), c.Signals...)}, true
}

// Starts counts processes actually launched in the session's sandbox (idempotent re-starts excluded).
func (f *Fleet) Starts(sessionID uuid.UUID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.boxes[sessionID]; ok {
		return len(b.cmds)
	}
	return 0
}

func (f *Fleet) File(sessionID uuid.UUID, path string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.boxes[sessionID]
	if !ok {
		return nil, false
	}
	d, ok := b.Files[path]
	return d, ok
}

// Client returns the sandboxd client for an endpoint built from Name. It satisfies ops.SandboxAPI.
func (f *Fleet) Client(endpoint, _ string) *Client {
	u, err := url.Parse(endpoint)
	if err != nil {
		panic(err)
	}
	id, err := uuid.Parse(strings.TrimPrefix(u.Hostname(), "sbx-"))
	if err != nil {
		panic(fmt.Sprintf("unexpected endpoint %q", endpoint))
	}
	return &Client{f: f, id: id}
}

// --- programs ---

type stepKind int

const (
	stepOut stepKind = iota
	stepPause
	stepGate
	stepWaitSignal
	stepExit
)

type step struct {
	kind       stepKind
	stream     string
	data       string
	gate       <-chan struct{}
	ignoreTerm bool
	code       int
}

// Program scripts a fake process. It runs eagerly from start until it reaches a Pause, an unopened
// Gate or WaitSignal. Each stdout Logs call consumes one Pause, so output arrives over several
// reads. A program that runs out of steps exits 0.
type Program []step

func Stdout(s string) step { return step{kind: stepOut, stream: "stdout", data: s} }
func Stderr(s string) step { return step{kind: stepOut, stream: "stderr", data: s} }

// Line is Stdout(s + "\n").
func Line(s string) step { return Stdout(s + "\n") }
func Pause() step        { return step{kind: stepPause} }

// Gate blocks the program until ch is closed.
func Gate(ch <-chan struct{}) step { return step{kind: stepGate, gate: ch} }

// WaitSignal blocks until TERM (exit 143) or KILL (exit 137). With ignoreTerm only KILL ends it.
func WaitSignal(ignoreTerm bool) step { return step{kind: stepWaitSignal, ignoreTerm: ignoreTerm} }
func Exit(code int) step              { return step{kind: stepExit, code: code} }

// run advances until the program blocks or exits. consumePause lets one Pause through.
func (c *Cmd) run(consumePause bool) {
	for c.exitCode == nil {
		if c.pc >= len(c.prog) {
			c.exit(0)
			return
		}
		s := c.prog[c.pc]
		switch s.kind {
		case stepOut:
			if s.stream == "stderr" {
				c.stderr = append(c.stderr, s.data...)
			} else {
				c.stdout = append(c.stdout, s.data...)
			}
		case stepPause:
			if !consumePause {
				return
			}
			consumePause = false
		case stepGate:
			select {
			case <-s.gate:
			default:
				return
			}
		case stepWaitSignal:
			return
		case stepExit:
			c.exit(s.code)
			return
		}
		c.pc++
	}
}

func (c *Cmd) exit(code int) { c.exitCode = &code }

func (c *Cmd) status(id string) sandboxclient.Command {
	st := sandboxclient.Command{ID: id, State: sandboxclient.StateRunning, StartedAt: c.started}
	if c.exitCode != nil {
		code := *c.exitCode
		st.State, st.ExitCode = sandboxclient.StateExited, &code
	}
	return st
}

// --- sandboxd client ---

type Client struct {
	f  *Fleet
	id uuid.UUID
}

const maxLogChunk = 1 << 20

// box returns the sandbox if reachable. Caller holds f.mu.
func (c *Client) box(ctx context.Context) (*Box, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, ok := c.f.boxes[c.id]
	if !ok || !b.Running {
		return nil, errUnreachable
	}
	return b, nil
}

func (c *Client) cmd(ctx context.Context, id string) (*Cmd, error) {
	b, err := c.box(ctx)
	if err != nil {
		return nil, err
	}
	cmd, ok := b.cmds[id]
	if !ok {
		return nil, sandboxclient.ErrNotFound
	}
	return cmd, nil
}

func (c *Client) Health(ctx context.Context) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	_, err := c.box(ctx)
	return err
}

func (c *Client) PutFile(ctx context.Context, path string, data []byte, _ fs.FileMode) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	b, err := c.box(ctx)
	if err != nil {
		return err
	}
	b.Files[path] = append([]byte(nil), data...)
	return nil
}

func (c *Client) StartCommand(ctx context.Context, req sandboxclient.CommandRequest) (sandboxclient.Command, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	b, err := c.box(ctx)
	if err != nil {
		return sandboxclient.Command{}, err
	}
	if cmd, ok := b.cmds[req.ID]; ok {
		return cmd.status(req.ID), nil
	}
	var prog Program
	if c.f.Program != nil {
		prog = c.f.Program(c.id, req)
	}
	cmd := &Cmd{Argv: append([]string(nil), req.Argv...), prog: prog, started: time.Now()}
	b.cmds[req.ID] = cmd
	cmd.run(false)
	return cmd.status(req.ID), nil
}

func (c *Client) Command(ctx context.Context, id string) (sandboxclient.Command, error) {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	cmd, err := c.cmd(ctx, id)
	if err != nil {
		return sandboxclient.Command{}, err
	}
	cmd.run(false)
	return cmd.status(id), nil
}

// Logs follows sandboxd: at most 1 MiB per read, and the state is sampled before reading, so
// "exited" with no data means the stream is complete.
func (c *Client) Logs(ctx context.Context, id, stream string, offset int64) ([]byte, int64, string, error) {
	if hook := c.f.OnLogs; hook != nil {
		hook(ctx, c.id, id, stream, offset)
	}
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	cmd, err := c.cmd(ctx, id)
	if err != nil {
		return nil, 0, "", err
	}
	if stream == "stdout" {
		cmd.run(true)
	} else {
		cmd.run(false)
	}
	state := cmd.status(id).State
	buf := cmd.stdout
	if stream == "stderr" {
		buf = cmd.stderr
	}
	if offset > int64(len(buf)) {
		offset = int64(len(buf))
	}
	data := buf[offset:min(int64(len(buf)), offset+maxLogChunk)]
	return append([]byte(nil), data...), offset + int64(len(data)), state, nil
}

func (c *Client) Signal(ctx context.Context, id, signal string) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	cmd, err := c.cmd(ctx, id)
	if err != nil {
		return err
	}
	if cmd.exitCode != nil {
		return sandboxclient.ErrExited
	}
	cmd.Signals = append(cmd.Signals, signal)
	ignoreTerm := cmd.pc < len(cmd.prog) && cmd.prog[cmd.pc].kind == stepWaitSignal && cmd.prog[cmd.pc].ignoreTerm
	switch {
	case signal == "KILL":
		cmd.exit(137)
	case signal == "TERM" && !ignoreTerm:
		cmd.exit(143)
	}
	return nil
}
