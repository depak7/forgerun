// The API's JSON shapes (internal/api/api.go). Kept by hand: they're a
// contract, and the Go tests pin the field names.

export type TaskState = "queued" | "running" | "succeeded" | "failed" | "cancelled";

export interface Status {
  pull_requests: { enabled: boolean; github_user: string };
  snapshots: { enabled: boolean; count: number; bytes: number; budget_bytes: number };
  tasks: { queued: number; running: number };
}

export interface Agent {
  id: string;
  name: string;
  default_model: string;
  runnable: boolean;
  credential: { name: string; set: boolean } | null;
}

export interface Task {
  id: string;
  repo: string;
  prompt: string;
  agent: string;
  model: string; // family, e.g. "claude-opus-5-5" or "auto"
  effort: string;
  thinking: boolean;
  fast: boolean;
  model_id: string;
  state: TaskState;
  error: string;
  has_patch: boolean;
  snapshot_id: number | null; // null = cold start
  parent_id: string | null; // null for a thread's first task
  thread_id: string;
  pr: { requested: boolean; url: string; number: number; branch: string; error: string };
  summary: string;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
}

export interface Thread {
  id: string; // the first task's id
  title: string; // the first task's prompt
  repo: string;
  steps: number;
  agents: string[]; // each step's agent, oldest first
  latest: Task;
  needs_attention: boolean;
  updated_at: string;
}
