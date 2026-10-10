// Shapes from docs/contracts/api.md.

export type SessionStatus =
  | "pending"
  | "provisioning"
  | "idle"
  | "running"
  | "stopping"
  | "complete"
  | "failed"
  | "stopped";

// Equal to SessionStatus since complete/failed/stopped became terminal statuses.
export type DisplayStatus = SessionStatus;

export const DISPLAY_STATUSES: DisplayStatus[] = [
  "pending",
  "provisioning",
  "idle",
  "running",
  "stopping",
  "complete",
  "failed",
  "stopped",
];

export type TurnStatus =
  | "queued"
  | "running"
  | "complete"
  | "failed"
  | "stopped"
  | "limit_exceeded";

export type Harness = "claude-code" | "codex";

export const HARNESSES: Harness[] = ["claude-code", "codex"];

export interface Usage {
  input_tokens: number;
  output_tokens: number;
  cached_input_tokens: number;
  agent_steps: number;
}

export interface Turn {
  seq: number;
  prompt: string;
  status: TurnStatus;
  result?: string;
  error?: string;
  usage?: Usage;
  created_at: string;
  started_at?: string;
  ended_at?: string;
}

export interface Session {
  id: string;
  owner: string;
  name: string;
  status: SessionStatus;
  display_status: DisplayStatus;
  harness: Harness;
  preset: string;
  lifecycle?: Lifecycle;
  repo?: string;
  model?: string;
  tokens_used: number;
  token_budget: number;
  cost_usd: number;
  cost_complete: boolean;
  duration_seconds: number;
  error?: string;
  created_at: string;
  updated_at: string;
  expires_at: string;
  idle_until?: string;
  ended_at?: string;
  turn_count: number;
  current_turn?: number;
  turns?: Turn[];
}

export type EventKind = "lifecycle" | "harness" | "stderr" | "egress" | "system";

export interface SessionEvent {
  id: number;
  session_id: string;
  kind: EventKind | string;
  turn_seq?: number | null;
  payload: Record<string, unknown>;
  ts: string;
}

export interface SessionList {
  sessions: Session[];
  total: number;
}

export interface Stats {
  total: number;
  running: number;
  active: number;
  failed: number;
  failure_rate: number;
}

export interface EgressPayload {
  decision: "allowed" | "denied";
  via: "connect" | "inference" | "github";
  host: string;
  method?: string;
  path?: string;
  reason?: string;
}

// Sessions that accept no more work (stopping is on its way to one of the terminal statuses).
export const TERMINAL_SESSION: SessionStatus[] = ["stopping", "complete", "failed", "stopped"];

export const ENDED_SESSION: SessionStatus[] = ["complete", "failed", "stopped"];

export type LifecycleAction = "terminate" | "keep_idle";

export interface Lifecycle {
  on_complete: LifecycleAction;
  on_failure: LifecycleAction;
  on_stop: LifecycleAction;
}
