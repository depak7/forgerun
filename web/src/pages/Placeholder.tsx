// A stand-in for the screens built in the next steps.

export function Pending({ title }: { title: string }) {
  return (
    <header style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      <span className="label">Coming next</span>
      <h1 style={{ margin: 0, fontSize: 32, fontWeight: 500, letterSpacing: -0.8 }}>{title}</h1>
    </header>
  );
}
