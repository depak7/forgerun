// The API's JSON shapes (internal/api/api.go). Kept by hand: they're a
// contract, and the Go tests pin the field names.

export type TaskState = "queued" | "running" | "succeeded" | "failed" | "cancelled";

export interface Status {
  pull_requests: { enabled: boolean; github_user: string };
  snapshots: {
    enabled: boolean;
    count: number;
    bytes: number;
    budget_bytes: number; // 0 = no limit
    max_age_seconds: number; // 0 = never expire
    sweep_seconds: number;
  };
  require_zdr: boolean; // only zero-data-retention models are accepted
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

// A model family offered for an agent (GET /api/models?agent=…).
export interface ModelFamily {
  id: string;
  name: string;
  efforts: string[]; // [] when the family has none
  default_effort: string; // "" = the plain variant
  thinking: boolean;
  fast: boolean;
  zdr: boolean;
}

export interface ModelList {
  agent: string;
  runnable: boolean;
  default: string; // default family
  models: ModelFamily[];
}

export interface Snapshot {
  id: number;
  repo: string; // normalized
  agent: string;
  commit: string;
  state: "current" | "retired";
  size_bytes: number;
  created_at: string;
  last_used_at: string;
}

// The body of POST /api/tasks.
export interface NewTask {
  repo: string;
  prompt: string;
  agent: string;
  model: string;
  effort?: string;
  thinking?: boolean;
  fast?: boolean;
  pr: boolean;
}
