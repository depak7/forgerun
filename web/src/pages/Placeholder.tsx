import { useStatus } from "../api/queries";

// Stand-ins for the screens built in the next steps. Tasks shows live
// numbers from the API, which proves the whole chain works.

export function TasksPage() {
  const { data: status, isLoading } = useStatus();
  return (
    <>
      <header style={{ display: "flex", flexDirection: "column", gap: 10 }}>
        <span className="label">01 | Tasks</span>
        <h1 style={{ margin: 0, fontSize: 32, fontWeight: 500, letterSpacing: -0.8 }}>Agents at work</h1>
        <p style={{ margin: 0, color: "var(--muted)" }}>Each task runs in its own VM and ends as a patch or a pull request.</p>
      </header>
      <p className="mono" style={{ color: "var(--muted)" }}>
        {isLoading ? "loading…" : `${status?.tasks.running ?? 0} running, ${status?.tasks.queued ?? 0} queued. The task list comes next.`}
      </p>
    </>
  );
}

export function Pending({ title }: { title: string }) {
  return (
    <header style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      <span className="label">Coming next</span>
      <h1 style={{ margin: 0, fontSize: 32, fontWeight: 500, letterSpacing: -0.8 }}>{title}</h1>
    </header>
  );
}
