import type { Agent, ModelFamily } from "../api/types";

// KeyState is how an agent's credential stands where its tasks run: set,
// missing, or optional (missing, but some of its models run without it).
export type KeyState = "set" | "optional" | "missing" | "none";

export function keyState(a: Agent | undefined): KeyState {
  if (!a?.credential) return "none";
  if (a.credential.set) return "set";
  return a.credential.optional ? "optional" : "missing";
}

// keyLabel is the state as the UI shows it, e.g. "KEY OPTIONAL".
export function keyLabel(s: KeyState): string {
  return s === "set" ? "KEY SET" : s === "optional" ? "KEY OPTIONAL" : s === "missing" ? "KEY MISSING" : "";
}

// blockedByKey says whether a task on model m would be refused for want of
// the agent's key: the server refuses only models that need it.
export function blockedByKey(a: Agent | undefined, m: ModelFamily | undefined): boolean {
  return !!a?.credential && !a.credential.set && !m?.keyless;
}
