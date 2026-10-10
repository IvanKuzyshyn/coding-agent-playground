package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"terrarium/internal/harness"
	"terrarium/internal/sandboxclient"
	"terrarium/internal/session"
	"terrarium/internal/store"
)

const (
	maxChunk           = 1 << 20
	maxStderrLineBytes = 4096
)

func commandID(seq int) string { return fmt.Sprintf("turn-%d", seq) }

// RunTurn starts a queued turn or re-attaches to a running one, streams its output into
// session events until the harness exits, and writes the turn's terminal state exactly once.
// Re-attachment uses the command id and log offsets stored on the turn row.
func (o *Ops) RunTurn(ctx context.Context, s store.Session, t store.Turn) error {
	c := o.client(s)
	cmdID := commandID(t.Seq)
	seq := t.Seq

	if t.Status == session.TurnQueued {
		if _, err := o.Store.SetStatus(ctx, s.ID, []string{session.StatusIdle}, session.StatusRunning, fmt.Sprintf("turn %d started", seq)); err != nil {
			return err
		}
		if err := o.Store.SetIdleUntil(ctx, s.ID, nil); err != nil {
			return err
		}
		// Record the command id before starting, so a crash in between re-attaches instead of
		// starting a second harness.
		if _, err := o.Store.StartTurn(ctx, s.ID, seq, cmdID); err != nil {
			return err
		}
		o.event(ctx, s.ID, store.EventLifecycle, &seq, map[string]string{"turn_status": session.TurnRunning})
	}

	// Idempotent in sandboxd: an existing command with this id is returned, not restarted.
	if _, err := c.StartCommand(ctx, sandboxclient.CommandRequest{
		ID:   cmdID,
		Argv: harness.TurnArgv(s.Spec, t.Prompt, s.HarnessSession),
		Cwd:  harness.WorkDir,
	}); err != nil {
		return err
	}

	tl := tail{o: o, c: c, s: s, seq: seq, cmdID: cmdID, outOff: t.LogOffset, errOff: t.ErrOffset}
	if err := tl.run(ctx); err != nil {
		return err
	}
	return o.finishTurn(ctx, s, t, tl)
}

type tail struct {
	o      *Ops
	c      SandboxAPI
	s      store.Session
	seq    int
	cmdID  string
	outOff int64
	errOff int64

	final       *harness.Update
	lastMessage string
	stopReason  string
	limitHit    bool
	termSentAt  time.Time
}

func (tl *tail) run(ctx context.Context) error {
	for {
		outN, outDone, state, err := tl.readStream(ctx, "stdout")
		if err != nil {
			return err
		}
		errN, errDone, _, err := tl.readStream(ctx, "stderr")
		if err != nil {
			return err
		}
		if outN+errN > 0 {
			if err := tl.o.Store.SetTurnOffsets(ctx, tl.s.ID, tl.seq, tl.outOff, tl.errOff); err != nil {
				return err
			}
		}
		if state == "exited" && outDone && errDone {
			return nil
		}
		if state != "exited" {
			if err := tl.enforce(ctx); err != nil {
				return err
			}
		}
		if outN+errN == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(tl.o.PollInterval):
			}
		}
	}
}

// readStream consumes complete lines from one stream. done reports that the command has exited
// and everything up to the end of the stream has been consumed.
func (tl *tail) readStream(ctx context.Context, stream string) (consumed int, done bool, state string, err error) {
	off := &tl.outOff
	if stream == "stderr" {
		off = &tl.errOff
	}
	data, _, state, err := tl.c.Logs(ctx, tl.cmdID, stream, *off)
	if err != nil {
		return 0, false, "", err
	}
	lines, n := splitLines(data)
	// A full chunk without a newline, or trailing bytes after exit, is still one line.
	if (n == 0 && len(data) >= maxChunk) || (state == "exited" && n < len(data)) {
		lines = append(lines, data[n:])
		n = len(data)
	}
	for _, line := range lines {
		if stream == "stdout" {
			tl.handleStdout(ctx, line)
		} else if len(strings.TrimSpace(string(line))) > 0 {
			if len(line) > maxStderrLineBytes {
				line = line[:maxStderrLineBytes]
			}
			tl.o.event(ctx, tl.s.ID, store.EventStderr, &tl.seq, map[string]string{"line": string(line)})
		}
	}
	*off += int64(n)
	return n, state == "exited" && n == len(data), state, nil
}

func (tl *tail) handleStdout(ctx context.Context, line []byte) {
	u, raw, ok := harness.ParseLine(tl.s.Harness, line)
	if !ok {
		if len(strings.TrimSpace(string(line))) > 0 {
			tl.o.event(ctx, tl.s.ID, store.EventSystem, &tl.seq, map[string]string{"message": string(line)})
		}
		return
	}
	tl.o.event(ctx, tl.s.ID, store.EventHarness, &tl.seq, map[string]any{"harness": tl.s.Harness, "event": raw})
	if u.HarnessSession != "" && tl.s.HarnessSession == "" {
		if err := tl.o.Store.SetHarnessSession(ctx, tl.s.ID, u.HarnessSession); err != nil {
			tl.o.Log.Warn("set harness session", "session", tl.s.ID, "err", err)
		}
		tl.s.HarnessSession = u.HarnessSession
	}
	if u.Message != "" {
		tl.lastMessage = u.Message
	}
	if u.Final {
		tl.final = &u
	}
}

// enforce stops the harness when the engineer asked, the session is ending, the TTL passed or
// the token budget is spent: TERM first, KILL after the grace period.
func (tl *tail) enforce(ctx context.Context) error {
	if tl.stopReason == "" {
		cur, err := tl.o.Store.GetSession(ctx, tl.s.ID)
		if err != nil {
			return err
		}
		turn, err := tl.o.Store.GetTurn(ctx, tl.s.ID, tl.seq)
		if err != nil {
			return err
		}
		switch {
		case cur.Status == session.StatusStopping:
			tl.stopReason = "session stopping"
		case turn.StopRequested:
			tl.stopReason = "stopped by engineer"
		case time.Now().After(cur.ExpiresAt):
			tl.stopReason = "session ttl expired"
		case cur.Spec.Limits.TokenBudget > 0 && cur.TokensUsed >= cur.Spec.Limits.TokenBudget:
			tl.stopReason = "session token budget exhausted"
			tl.limitHit = true
		}
	}
	if tl.stopReason == "" {
		return nil
	}
	switch {
	case tl.termSentAt.IsZero():
		tl.termSentAt = time.Now()
		tl.o.event(ctx, tl.s.ID, store.EventSystem, &tl.seq, map[string]string{"message": "stopping turn: " + tl.stopReason})
		return ignoreExited(tl.c.Signal(ctx, tl.cmdID, "TERM"))
	case time.Since(tl.termSentAt) > tl.o.StopGrace:
		return ignoreExited(tl.c.Signal(ctx, tl.cmdID, "KILL"))
	}
	return nil
}

func ignoreExited(err error) error {
	// The process may exit between the log read and the signal; the next log read observes it.
	if errors.Is(err, sandboxclient.ErrExited) {
		return nil
	}
	return err
}

func (o *Ops) finishTurn(ctx context.Context, s store.Session, t store.Turn, tl tail) error {
	cmd, err := tl.c.Command(ctx, tl.cmdID)
	if err != nil {
		return err
	}
	exit := -1
	if cmd.ExitCode != nil {
		exit = *cmd.ExitCode
	}

	status, errMsg := session.TurnComplete, ""
	var usage any
	if tl.final != nil && tl.final.Usage != nil {
		usage = tl.final.Usage
	}
	switch {
	case tl.limitHit:
		status, errMsg = session.TurnLimitExceeded, tl.stopReason
	case tl.stopReason != "":
		status, errMsg = session.TurnStopped, tl.stopReason
	case tl.final == nil:
		status, errMsg = session.TurnFailed, fmt.Sprintf("harness exited %d without a result", exit)
	case tl.final.Failed && strings.HasPrefix(tl.final.Error, "error_max_turns"):
		status, errMsg = session.TurnLimitExceeded, "max agent steps reached"
	case tl.final.Failed:
		status, errMsg = session.TurnFailed, tl.final.Error
	}
	if _, err := o.Store.FinishTurn(ctx, s.ID, t.Seq, status, tl.lastMessage, errMsg, usage); err != nil {
		return err
	}
	seq := t.Seq
	o.event(ctx, s.ID, store.EventLifecycle, &seq, map[string]string{"turn_status": status})

	if tl.stopReason == "session ttl expired" {
		_, err := o.Store.SetStatus(ctx, s.ID, []string{session.StatusRunning}, session.StatusStopping, tl.stopReason)
		return err
	}
	return o.becomeIdle(ctx, s, []string{session.StatusRunning}, fmt.Sprintf("turn %d %s", seq, status))
}

// IsLost reports whether the session's sandbox no longer exists or is not running.
func (o *Ops) IsLost(ctx context.Context, s store.Session) (bool, error) {
	sb, err := o.Sandboxes.Get(ctx, s.ID)
	if err != nil {
		if errors.Is(err, errSandboxNotFound) {
			return true, nil
		}
		return false, err
	}
	return !sb.Running, nil
}
