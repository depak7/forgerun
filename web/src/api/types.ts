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
