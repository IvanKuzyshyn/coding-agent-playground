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
	StatusStopped      = "stopped"
	StatusFailed       = "failed"
)

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
	return status == StatusStopped
}

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
