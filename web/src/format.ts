// formatBytes shows a size the way the server's logs do: "26.6 MB", "20 GB".
export function formatBytes(bytes: number): string {
  if (bytes >= 1e9) return `${trim(bytes / 1e9)} GB`;
  if (bytes >= 1e6) return `${trim(bytes / 1e6)} MB`;
  if (bytes >= 1e3) return `${trim(bytes / 1e3)} KB`;
  return `${bytes} B`;
}

function trim(n: number): string {
  return n.toFixed(1).replace(/\.0$/, "");
}

// formatElapsed shows a duration as "2m 14s", "1h 3m" or "45s".
export function formatElapsed(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

// formatWhen shows when something happened: "now", "13:44" today,
// "yesterday", or "Oct 12" further back.
export function formatWhen(iso: string, now: Date = new Date()): string {
  const t = new Date(iso);
  const ago = now.getTime() - t.getTime();
  if (ago < 60_000) return "now";
  if (sameDay(t, now)) return t.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDay(t, yesterday)) return "yesterday";
  return t.toLocaleDateString([], { month: "short", day: "numeric" });
}

function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

// shortID shortens a task id for display: t-20261015-134423-629e08 → t-…629e08.
export function shortID(id: string): string {
  return id.length > 12 ? `t-…${id.slice(-6)}` : id;
}

// repoName shows a repo URL as owner/name: https://github.com/a/b.git → a/b.
// Other hosts keep their host so they're not mistaken for GitHub.
export function repoName(url: string): string {
  const m = /^https?:\/\/([^/]+)\/(.+?)(?:\.git)?\/?$/.exec(url.trim());
  if (!m) return url;
  return m[1].toLowerCase() === "github.com" ? m[2] : `${m[1]}/${m[2]}`;
}

// agentChain shows a thread's agents in order, collapsing repeats:
// ["cursor","cursor","claude-code"] → "cursor ×2 → claude-code".
export function agentChain(agents: string[]): string {
  const runs: { agent: string; n: number }[] = [];
  for (const a of agents) {
    const last = runs[runs.length - 1];
    if (last && last.agent === a) last.n++;
    else runs.push({ agent: a, n: 1 });
  }
  return runs.map((r) => (r.n > 1 ? `${r.agent} ×${r.n}` : r.agent)).join(" → ");
}
