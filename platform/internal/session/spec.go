// Package session holds the session domain: the frozen SessionSpec, statuses and the state machine.
package session

// Harness identifiers.
const (
	HarnessClaude = "claude-code"
	HarnessCodex  = "codex"
)

// Session statuses. See docs/plan/local-prototype-plan.md §2.2.
const (
	StatusPending      = "pending"
	StatusProvisioning = "provisioning"
	StatusIdle         = "idle"
	StatusRunning      = "running"
	StatusStopping     = "stopping"
	// Terminal statuses. A session reaches one only after its sandbox has been destroyed.
	StatusComplete = "complete" // its work finished without failure
	StatusFailed   = "failed"
	StatusStopped  = "stopped" // ended by an engineer, a timeout, or a stopped turn
)

// TerminalStatuses lists the statuses a session never leaves.
var TerminalStatuses = []string{StatusComplete, StatusFailed, StatusStopped}

// Turn statuses.
const (
	TurnQueued        = "queued"
	TurnRunning       = "running"
	TurnComplete      = "complete"
	TurnFailed        = "failed"
	TurnStopped       = "stopped"
	TurnLimitExceeded = "limit_exceeded"
)

// Live reports whether a session may use the gateway and receive turns.
func Live(status string) bool {
	return status == StatusIdle || status == StatusRunning
}

// Terminal reports whether a session has ended.
func Terminal(status string) bool {
	return status == StatusComplete || status == StatusFailed || status == StatusStopped
}

// Lifecycle actions.
const (
	ActionTerminate = "terminate" // end the session and destroy the sandbox
	ActionKeepIdle  = "keep_idle" // keep the sandbox waiting for follow-ups
)

// Lifecycle says what happens when a turn ends and no follow-up is queued.
type Lifecycle struct {
	OnComplete string `json:"on_complete"` // turn finished successfully → session complete
	OnFailure  string `json:"on_failure"`  // turn failed or hit a limit → session failed
	OnStop     string `json:"on_stop"`     // turn stopped by the engineer → session stopped
}

// KeepIdleLifecycle keeps the sandbox after every outcome (POST /sessions "keep_idle": true).
var KeepIdleLifecycle = Lifecycle{OnComplete: ActionKeepIdle, OnFailure: ActionKeepIdle, OnStop: ActionKeepIdle}

// Action returns the rule for a turn's terminal status.
func (l Lifecycle) Action(turnStatus string) string {
	switch turnStatus {
	case TurnComplete:
		return l.OnComplete
	case TurnFailed, TurnLimitExceeded:
		return l.OnFailure
	default:
		return l.OnStop
	}
}

// FinalStatus maps a turn's terminal status to the session status it ends with.
func FinalStatus(turnStatus string) string {
	switch turnStatus {
	case TurnComplete:
		return StatusComplete
	case TurnFailed, TurnLimitExceeded:
		return StatusFailed
	default:
		return StatusStopped
	}
}

func ValidAction(a string) bool { return a == ActionTerminate || a == ActionKeepIdle }

// Injection guard modes: what the gateway does when new tool output in an inference request
// matches an injection rule.
const (
	GuardOff     = "off"
	GuardMonitor = "monitor" // record a flagged egress event and forward the request
	GuardBlock   = "block"   // record a denied egress event and refuse the request
)

func ValidGuard(m string) bool { return m == GuardOff || m == GuardMonitor || m == GuardBlock }

// Spec is frozen when a session is created. The supervisor and gateway read only the Spec,
// never the catalog, so catalog edits affect only new sessions.
type Spec struct {
	Harness       string    `json:"harness"`
	Preset        string    `json:"preset"`
	Image         string    `json:"image"`
	Repo          string    `json:"repo,omitempty"`
	Model         string    `json:"model,omitempty"`
	Instructions  string    `json:"instructions,omitempty"`
	Resources     Resources `json:"resources"`
	Network       Network   `json:"network"`
	Limits        Limits    `json:"limits"`
	Lifecycle     Lifecycle `json:"lifecycle"`
	Guard         string    `json:"guard,omitempty"` // injection guard mode; empty (sessions created before it) is off
	CatalogDigest string    `json:"catalog_digest"`
}

type Resources struct {
	CPUs     float64 `json:"cpus"`
	MemoryMB int64   `json:"memory_mb"`
	PIDs     int64   `json:"pids"`
}

// Network lists the hosts the sandbox may reach through the gateway's CONNECT proxy (port 443 only).
type Network struct {
	Allow []string `json:"allow"`
}

type Limits struct {
	// MaxAgentSteps caps the harness's internal agent loop per turn (Claude --max-turns).
	MaxAgentSteps int `json:"max_agent_steps"`
	// TokenBudget caps input+output tokens across the whole session, enforced by the gateway.
	TokenBudget        int64 `json:"token_budget"`
	IdleTimeoutSeconds int   `json:"idle_timeout_seconds"`
	TTLSeconds         int   `json:"ttl_seconds"`
}

// EgressAllowed reports whether a session may use the CONNECT proxy. Provisioning needs it for
// git clone; inference requires Live.
func EgressAllowed(status string) bool {
	return status == StatusProvisioning || Live(status)
}

// BranchPrefix is the only branch namespace a session may push to and open pull requests from.
func BranchPrefix(sessionID string) string { return "terrarium/" + sessionID + "/" }
