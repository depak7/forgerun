import { Link } from "react-router-dom";

// NotFoundPage is any path the app has no page for.
export function NotFoundPage() {
  return (
    <header style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      <span className="label">404</span>
      <h1 style={{ margin: 0, fontSize: 32, fontWeight: 500, letterSpacing: -0.8 }}>No such page</h1>
      <p style={{ margin: 0, color: "var(--muted)" }}>
        <Link to="/">Back to tasks</Link>
      </p>
    </header>
  );
}
