package ops

import (
	"context"
	"encoding/json"
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
		changed, err := o.Store.SetStatus(ctx, s.ID, []string{session.StatusIdle}, session.StatusRunning, fmt.Sprintf("turn %d started", seq))
		if err != nil {
			return err
		}
		if !changed {
			// Already running means a crash after this step; anything else (e.g. stopping) means
			// the session must not start new work.
			cur, err := o.Store.GetSession(ctx, s.ID)
			if err != nil {
				return err
			}
			if cur.Status != session.StatusRunning {
				return nil
			}
		}
		if err := o.Store.SetIdleUntil(ctx, s.ID, nil); err != nil {
			return err
		}
		// Record the command id before starting, so a crash in between re-attaches instead of
		// starting a second harness.
		started, err := o.Store.StartTurn(ctx, s.ID, seq, cmdID)
		if err != nil {
			return err
		}
		if !started {
			// The turn was cancelled (session ending) between the read and here.
			return nil
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

	pending     []store.NewEvent // events not yet committed with their offsets
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
			// Events and offsets commit together: a crash either replays none or all of a batch.
			if err := tl.o.Store.AppendTurnOutput(ctx, tl.s.ID, tl.seq, tl.pending, tl.outOff, tl.errOff); err != nil {
				return err
			}
			tl.pending = tl.pending[:0]
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
	// A short read from an exited command reached the end of its output.
	atEnd := state == "exited" && len(data) < maxChunk
	// A full chunk without a newline, or trailing bytes at the end of output, is still one line.
	if (n == 0 && len(data) >= maxChunk) || (atEnd && n < len(data)) {
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
			tl.add(store.EventStderr, map[string]string{"line": string(line)})
		}
	}
	*off += int64(n)
	// Only an empty read after exit proves the stream is fully consumed (contract: sandboxd.md).
	return n, state == "exited" && len(data) == 0, state, nil
}

func (tl *tail) add(kind string, payload any) {
	seq := tl.seq
	tl.pending = append(tl.pending, store.NewEvent{Kind: kind, TurnSeq: &seq, Payload: payload})
}

func (tl *tail) handleStdout(ctx context.Context, line []byte) {
	u, raw, ok := harness.ParseLine(tl.s.Harness, line)
	if !ok {
		if len(strings.TrimSpace(string(line))) > 0 {
			tl.add(store.EventSystem, map[string]string{"message": string(line)})
		}
		return
	}
	tl.add(store.EventHarness, map[string]any{"harness": tl.s.Harness, "event": raw})
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
		prev, err := o.previousUsage(ctx, s, t.Seq)
		if err != nil {
			return err
		}
		usage = tl.final.Usage.PerTurn(prev)
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
	finished, err := o.Store.FinishTurn(ctx, s.ID, t.Seq, status, tl.lastMessage, errMsg, usage)
	if err != nil {
		return err
	}
	if !finished {
		// Another writer already ended this turn (e.g. cleanup cancelled it); it owns the session state.
		return nil
	}
	seq := t.Seq
	o.event(ctx, s.ID, store.EventLifecycle, &seq, map[string]string{"turn_status": status})

	if tl.stopReason == "session ttl expired" {
		_, err := o.Store.SetStatus(ctx, s.ID, []string{session.StatusRunning}, session.StatusStopping, tl.stopReason)
		return err
	}
	return o.BecomeIdle(ctx, s, []string{session.StatusRunning}, fmt.Sprintf("turn %d %s", seq, status))
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

// previousUsage returns the usage of the latest earlier turn that recorded one.
func (o *Ops) previousUsage(ctx context.Context, s store.Session, seq int) (*harness.Usage, error) {
	turns, err := o.Store.Turns(ctx, s.ID)
	if err != nil {
		return nil, err
	}
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Seq >= seq || len(turns[i].Usage) == 0 {
			continue
		}
		var u harness.Usage
		if err := json.Unmarshal(turns[i].Usage, &u); err != nil {
			return nil, err
		}
		return &u, nil
	}
	return nil, nil
}
