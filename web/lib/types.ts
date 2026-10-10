// Shapes from docs/contracts/api.md.

export type SessionStatus =
  | "pending"
  | "provisioning"
  | "idle"
  | "running"
  | "stopping"
  | "stopped"
  | "failed";

export type TurnStatus =
  | "queued"
  | "running"
  | "complete"
  | "failed"
  | "stopped"
  | "limit_exceeded";

export type Harness = "claude-code" | "codex";

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
  status: SessionStatus;
  harness: Harness;
  preset: string;
  repo?: string;
  model?: string;
  tokens_used: number;
  token_budget: number;
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

export interface Preset {
  name: string;
}

export interface EgressPayload {
  decision: "allowed" | "denied";
  via: "connect" | "inference";
  host: string;
  method?: string;
  path?: string;
  reason?: string;
}

export const TERMINAL_SESSION: SessionStatus[] = ["stopping", "stopped", "failed"];
