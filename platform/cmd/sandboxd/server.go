package main

import (
	"cmp"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"terrarium/internal/sandboxclient"
)

const (
	maxLogChunk = 1 << 20
	// How long Wait keeps draining a command's output pipes after the process exits, in case a
	// background descendant still holds them open.
	pipeDrainDelay = 2 * time.Second
	// Exit code reported when a command cannot be started (shell convention for "not found").
	startFailedCode = 127
)

var idRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

type server struct {
	token    []byte
	stateDir string
	workDir  string
	env      []string

	mu      sync.Mutex
	cmds    map[string]*proc
	closing bool
}

type proc struct {
	id        string
	pid       int
	startedAt time.Time
	done      chan struct{}

	mu       sync.Mutex
	exited   bool
	exitCode int
	endedAt  time.Time
}

// newServer captures the current environment as the base for child commands, so the token must
// already have been removed from it.
func newServer(token, stateDir, workDir string) *server {
	return &server{
		token:    []byte("Bearer " + token),
		stateDir: stateDir,
		workDir:  workDir,
		env:      os.Environ(),
		cmds:     map[string]*proc{},
	}
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.Handle("PUT /files", s.auth(s.putFile))
	mux.Handle("GET /files", s.auth(s.getFile))
	mux.Handle("POST /commands", s.auth(s.startCommand))
	mux.Handle("GET /commands/{id}", s.auth(s.getCommand))
	mux.Handle("GET /commands/{id}/logs", s.auth(s.getLogs))
	mux.Handle("POST /commands/{id}/signal", s.auth(s.signalCommand))
	return mux
}

func (s *server) auth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), s.token) != 1 {
			httpError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		h(w, r)
	})
}

func (s *server) putFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if !validPath(path) {
		httpError(w, http.StatusBadRequest, "path must be absolute and clean")
		return
	}
	mode := uint64(0o644)
	if m := r.URL.Query().Get("mode"); m != "" {
		var err error
		if mode, err = strconv.ParseUint(m, 8, 32); err != nil || mode > 0o777 {
			httpError(w, http.StatusBadRequest, "mode must be octal permission bits")
			return
		}
	}
	if err := writeFileAtomic(path, r.Body, os.FileMode(mode)); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeFileAtomic(path string, data io.Reader, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".sandboxd-put-*")
	if err != nil {
		return err
	}
	_, err = io.Copy(f, data)
	if err == nil {
		err = f.Chmod(mode)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

func (s *server) getFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if !validPath(path) {
		httpError(w, http.StatusBadRequest, "path must be absolute and clean")
		return
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		httpError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		httpError(w, http.StatusBadRequest, "not a regular file")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	io.Copy(w, f)
}

func (s *server) startCommand(w http.ResponseWriter, r *http.Request) {
	var req sandboxclient.CommandRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := validateRequest(req); err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Holding s.mu across the launch makes concurrent duplicate starts produce one process.
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.cmds[req.ID]; ok {
		writeJSON(w, http.StatusOK, p.status())
		return
	}
	if s.closing {
		httpError(w, http.StatusServiceUnavailable, "shutting down")
		return
	}
	p, err := s.launch(req)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.cmds[req.ID] = p
	writeJSON(w, http.StatusOK, p.status())
}

func validateRequest(req sandboxclient.CommandRequest) error {
	if !idRE.MatchString(req.ID) {
		return errors.New("id must match ^[A-Za-z0-9._-]{1,128}$")
	}
	if len(req.Argv) == 0 || req.Argv[0] == "" {
		return errors.New("argv must not be empty")
	}
	if req.Cwd != "" && !validPath(req.Cwd) {
		return errors.New("cwd must be absolute and clean")
	}
	for k := range req.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return fmt.Errorf("invalid env name %q", k)
		}
	}
	return nil
}

// launch starts the command. A command that fails to start (missing binary, bad cwd) is still
// recorded, as exited with code 127 and the error on its stderr log, so retries by id stay
// idempotent and the caller reads the reason the same way as any other failure.
func (s *server) launch(req sandboxclient.CommandRequest) (*proc, error) {
	stdout, err := os.OpenFile(s.logPath(req.ID, "stdout"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	stderr, err := os.OpenFile(s.logPath(req.ID, "stderr"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		stdout.Close()
		return nil, err
	}

	cmd := exec.Command(req.Argv[0], req.Argv[1:]...)
	cmd.Dir = cmp.Or(req.Cwd, s.workDir)
	cmd.Env = slices.Clone(s.env)
	for _, k := range slices.Sorted(maps.Keys(req.Env)) {
		cmd.Env = append(cmd.Env, k+"="+req.Env[k])
	}
	// Wrapping the files hides *os.File from exec, so it uses pipes plus copy goroutines and Wait
	// returns only once all output has been written to the log files.
	cmd.Stdout = fileWriter{stdout}
	cmd.Stderr = fileWriter{stderr}
	cmd.WaitDelay = pipeDrainDelay
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	p := &proc{id: req.ID, startedAt: time.Now().UTC(), done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "sandboxd: start %q: %v\n", req.Argv[0], err)
		stdout.Close()
		stderr.Close()
		p.finish(startFailedCode)
		return p, nil
	}
	p.pid = cmd.Process.Pid
	go func() {
		cmd.Wait() // the exit status is read from ProcessState; WaitDelay errors are not fatal
		stdout.Close()
		stderr.Close()
		p.finish(exitCode(cmd.ProcessState))
	}()
	return p, nil
}

func exitCode(ps *os.ProcessState) int {
	if ps == nil {
		return -1
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

func (p *proc) finish(code int) {
	p.mu.Lock()
	p.exited, p.exitCode, p.endedAt = true, code, time.Now().UTC()
	p.mu.Unlock()
	close(p.done)
}

func (p *proc) status() sandboxclient.Command {
	p.mu.Lock()
	defer p.mu.Unlock()
	c := sandboxclient.Command{ID: p.id, State: sandboxclient.StateRunning, StartedAt: p.startedAt}
	if p.exited {
		code, ended := p.exitCode, p.endedAt
		c.State, c.ExitCode, c.EndedAt = sandboxclient.StateExited, &code, &ended
	}
	return c
}

func (s *server) lookup(w http.ResponseWriter, r *http.Request) *proc {
	s.mu.Lock()
	p := s.cmds[r.PathValue("id")]
	s.mu.Unlock()
	if p == nil {
		httpError(w, http.StatusNotFound, "unknown command")
	}
	return p
}

func (s *server) getCommand(w http.ResponseWriter, r *http.Request) {
	if p := s.lookup(w, r); p != nil {
		writeJSON(w, http.StatusOK, p.status())
	}
}

func (s *server) getLogs(w http.ResponseWriter, r *http.Request) {
	p := s.lookup(w, r)
	if p == nil {
		return
	}
	stream := cmp.Or(r.URL.Query().Get("stream"), "stdout")
	if stream != "stdout" && stream != "stderr" {
		httpError(w, http.StatusBadRequest, "stream must be stdout or stderr")
		return
	}
	var offset int64
	if o := r.URL.Query().Get("offset"); o != "" {
		var err error
		if offset, err = strconv.ParseInt(o, 10, 64); err != nil || offset < 0 {
			httpError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
	}

	// The state must be sampled before reading: if it was already exited, the file is final and
	// an empty read really means the end.
	state := p.status().State
	f, err := os.Open(s.logPath(p.id, stream))
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(io.NewSectionReader(f, offset, maxLogChunk), maxLogChunk))
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Next-Offset", strconv.FormatInt(offset+int64(len(data)), 10))
	w.Header().Set("X-Command-State", state)
	w.Write(data)
}

func (s *server) signalCommand(w http.ResponseWriter, r *http.Request) {
	p := s.lookup(w, r)
	if p == nil {
		return
	}
	var body struct {
		Signal string `json:"signal"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	var sig syscall.Signal
	switch body.Signal {
	case "TERM":
		sig = syscall.SIGTERM
	case "KILL":
		sig = syscall.SIGKILL
	default:
		httpError(w, http.StatusBadRequest, `signal must be "TERM" or "KILL"`)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.exited {
		httpError(w, http.StatusConflict, "command exited")
		return
	}
	if err := syscall.Kill(-p.pid, sig); err != nil {
		// ESRCH: the group is gone but the waiter has not recorded the exit yet.
		if errors.Is(err, syscall.ESRCH) {
			httpError(w, http.StatusConflict, "command exited")
			return
		}
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// terminateAll refuses new commands, sends TERM to every running process group and waits up to
// timeout for them to exit.
func (s *server) terminateAll(timeout time.Duration) {
	s.mu.Lock()
	s.closing = true
	var waits []chan struct{}
	for _, p := range s.cmds {
		p.mu.Lock()
		if !p.exited {
			syscall.Kill(-p.pid, syscall.SIGTERM)
			waits = append(waits, p.done)
		}
		p.mu.Unlock()
	}
	s.mu.Unlock()

	deadline := time.After(timeout)
	for _, done := range waits {
		select {
		case <-done:
		case <-deadline:
			return
		}
	}
}

func (s *server) logPath(id, stream string) string {
	ext := ".out"
	if stream == "stderr" {
		ext = ".err"
	}
	return filepath.Join(s.stateDir, id+ext)
}

func validPath(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p
}

type fileWriter struct{ f *os.File }

func (w fileWriter) Write(b []byte) (int, error) { return w.f.Write(b) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
