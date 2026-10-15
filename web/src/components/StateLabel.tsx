import type { TaskState } from "../api/types";

// The color of each state, from the design: running is blue and pulses,
// done is the accent, failed is red, the rest are quiet.
const colors: Record<TaskState, string> = {
  queued: "var(--text)",
  running: "var(--running)",
  succeeded: "var(--accent)",
  failed: "var(--failed)",
  cancelled: "var(--faint)",
};

export function StateLabel({ state }: { state: TaskState }) {
  return (
    <span style={{ color: colors[state] }}>
      <span className={state === "running" ? "live" : undefined}>●</span> {state.toUpperCase()}
    </span>
  );
}
