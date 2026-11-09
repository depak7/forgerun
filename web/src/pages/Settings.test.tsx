import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Status } from "../api/types";
import { renderPage } from "../test-utils";
import { SettingsPage } from "./Settings";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const agents = [
  { id: "claude-code", name: "Claude Code", default_model: "sonnet", runnable: true, credential: { name: "CLAUDE_CODE_OAUTH_TOKEN", set: true, optional: false } },
  { id: "cursor", name: "Cursor", default_model: "auto", runnable: true, credential: { name: "CURSOR_API_KEY", set: false, optional: false } },
];
const claudeModels = {
  agent: "claude-code", runnable: true, default: "sonnet",
  models: [
    { id: "sonnet", name: "Sonnet", efforts: ["low", "medium", "high"], default_effort: "medium", thinking: true, fast: false, zdr: true, keyless: false },
    { id: "haiku", name: "Haiku", efforts: [], default_effort: "", thinking: false, fast: true, zdr: false, keyless: false },
  ],
};
const cursorModels = { agent: "cursor", runnable: true, default: "auto", models: [{ id: "auto", name: "Auto", efforts: [], default_effort: "", thinking: false, fast: false, zdr: true, keyless: false }] };

function status(over: Partial<Status> = {}): Status {
  return {
    pull_requests: { enabled: true, github_user: "depak7" },
    snapshots: { enabled: true, count: 1, bytes: 5e9, budget_bytes: 20e9, max_age_seconds: 604800, sweep_seconds: 600 },
    tasks: { queued: 0, running: 0 },
    require_zdr: false,
    remote_workers: false,
    paused: false,
    limits: { turns_per_day: 20, live: 1 },
    sessions: { open: 1, max: 4, idle_seconds: 1800 },
    ...over,
  };
}
const snapshots = [
  { id: 1, repo: "github.com/depak7/playground", agent: "cursor", commit: "abcdef1234", state: "current", size_bytes: 5e9, created_at: "2026-10-14T10:00:00Z", last_used_at: "2026-10-14T10:00:00Z" },
];

// serve answers the page's requests; the model list depends on ?agent=.
// extra adds routes or replaces them.
function serve(st: Status, extra: Record<string, unknown> = {}) {
  const routes: Record<string, unknown> = {
    "/api/agents": agents,
    "/api/status": st,
    "/api/snapshots": snapshots,
    "/api/models?agent=claude-code": claudeModels,
    "/api/models?agent=cursor": cursorModels,
    ...extra,
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = input.toString();
      if (!(url in routes)) return new Response("not found", { status: 404 });
      return new Response(JSON.stringify({ success: true, data: routes[url] }), { status: 200 });
    }),
  );
}

describe("SettingsPage", () => {
  it("shows each agent's key, and how to fix a missing one", async () => {
    serve(status());
    renderPage(<SettingsPage />);
    expect(await screen.findByText("CURSOR_API_KEY")).toBeTruthy();
    expect(screen.getByText("KEY MISSING")).toBeTruthy();
    expect(screen.getByText("KEY SET")).toBeTruthy();
    expect(screen.getByText(/CURSOR_API_KEY=…/)).toBeTruthy();
  });

  it("lists each agent's models with effort and ZDR", async () => {
    serve(status({ require_zdr: true }));
    renderPage(<SettingsPage />);
    expect(await screen.findByText("low · medium* · high")).toBeTruthy();
    expect(screen.getByText(/accepts only models with zero data retention/)).toBeTruthy();
    const haiku = screen.getByText("haiku").closest("tr")!;
    expect(within(haiku).getByText("no")).toBeTruthy(); // not ZDR
    expect(await screen.findByText("1 model · 1 with ZDR · default auto")).toBeTruthy(); // cursor's list loaded too
    expect(screen.getByText("2 models · 1 with ZDR · default sonnet")).toBeTruthy();
  });

  it("says a key is optional when free models run without it", async () => {
    const opencode = { id: "opencode", name: "OpenCode", default_model: "big-pickle", runnable: true, credential: { name: "OPENCODE_API_KEY", set: false, optional: true } };
    const opencodeModels = {
      agent: "opencode", runnable: true, default: "big-pickle",
      models: [
        { id: "big-pickle", name: "Big Pickle (free)", efforts: [], default_effort: "", thinking: false, fast: false, zdr: false, keyless: true },
        { id: "kimi-k3", name: "Kimi K3", efforts: [], default_effort: "", thinking: false, fast: false, zdr: false, keyless: false },
      ],
    };
    serve(status(), { "/api/agents": [opencode], "/api/models?agent=opencode": opencodeModels });
    renderPage(<SettingsPage />);
    expect(await screen.findByText("KEY OPTIONAL")).toBeTruthy();
    expect(screen.queryByText("KEY MISSING")).toBeNull();
    expect(screen.getByText(/Free models run without it/)).toBeTruthy();
    expect(await screen.findByText("2 models · 0 with ZDR · 1 need no key · default big-pickle")).toBeTruthy();
  });

  it("explains pull requests that are off", async () => {
    serve(status({ pull_requests: { enabled: false, github_user: "" } }));
    renderPage(<SettingsPage />);
    expect(await screen.findByText(/Tasks can't push their changes/)).toBeTruthy();
  });

  it("shows snapshot usage, limits and the list", async () => {
    serve(status());
    renderPage(<SettingsPage />);
    expect(await screen.findByText(/5 GB used of 20 GB · 1 snapshot$/)).toBeTruthy();
    expect(screen.getByRole("meter", { name: "Snapshot disk used" }).getAttribute("aria-valuenow")).toBe("25");
    expect(screen.getByText(/unused for 7 days are deleted/)).toBeTruthy();
    expect(screen.getByText(/Checked every 10 min/)).toBeTruthy();
    expect(await screen.findByText("abcdef1")).toBeTruthy();
  });

  it("explains sessions", async () => {
    serve(status());
    renderPage(<SettingsPage />);
    expect(await screen.findByText("1 open of 4")).toBeTruthy();
    expect(screen.getByText(/closes after 30 min without a message/)).toBeTruthy();
  });

  it("lists connected workers when they run apart", async () => {
    const routes = { "/api/workers": [{ name: "deepak-mac", last_seen: new Date().toISOString(), agents: ["claude-code", "cursor"], adapters: ["claude-code", "cursor", "opencode"], sandbox: "gvisor", sessions: 1, max_sessions: 4, running: 1, capacity: { total: { cpus: 8, mem_mib: 16384 }, reserved: { cpus: 1, mem_mib: 1024 }, promised: { cpus: 2, mem_mib: 2048 } } }] };
    serve(status({ remote_workers: true }), routes);
    renderPage(<SettingsPage />);
    expect(await screen.findByText("deepak-mac")).toBeTruthy();
    expect(screen.getByText("CONNECTED")).toBeTruthy();
    expect(screen.getByText(/1 running · 1 session open of 4 · sandboxes: gVisor/)).toBeTruthy();
    expect(screen.getByText("free 5 CPUs · 13 GiB of 7 CPUs · 15 GiB")).toBeTruthy();
  });

  it("says when a worker has no agent keys", async () => {
    const routes = { "/api/workers": [{ name: "bare", last_seen: new Date().toISOString(), agents: [], adapters: ["opencode"], sandbox: "microsandbox", sessions: 0, max_sessions: 4, running: 0 }] };
    serve(status({ remote_workers: true }), routes);
    renderPage(<SettingsPage />);
    expect(await screen.findByText("no agent keys · models that need none")).toBeTruthy();
  });

  it("shows a worker's forgerun version, and none for a worker too old to report it", async () => {
    const seen = new Date().toISOString();
    const routes = {
      "/api/workers": [
        { name: "new-mac", last_seen: seen, agents: ["cursor"], adapters: ["cursor"], sandbox: "microsandbox", sessions: 0, max_sessions: 4, running: 0, version: "v0.4.2" },
        { name: "old-mac", last_seen: seen, agents: ["cursor"], adapters: ["cursor"], sandbox: "microsandbox", sessions: 0, max_sessions: 4, running: 0 },
      ],
    };
    serve(status({ remote_workers: true }), routes);
    renderPage(<SettingsPage />);
    const newRow = (await screen.findByText("new-mac")).closest("div")!.parentElement!;
    expect(within(newRow).getByText(/forgerun v0\.4\.2/)).toBeTruthy();
    const oldRow = (await screen.findByText("old-mac")).closest("div")!.parentElement!;
    expect(within(oldRow).queryByText(/forgerun v/)).toBeNull();
  });

  it("shows the limits and pauses new tasks", async () => {
    serve(status());
    renderPage(<SettingsPage />);
    expect(await screen.findByText(/may have 1 turn waiting or running, and start 20 turns a day/)).toBeTruthy();
    const fetchMock = globalThis.fetch as unknown as { mock: { calls: [RequestInfo, RequestInit?][] } };
    fireEvent.click(screen.getByRole("button", { name: /pause new tasks/ }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u, i]) => u.toString() === "/api/admin/pause" && i?.method === "POST")).toBe(true));
  });

  it("says when snapshots are off", async () => {
    serve(status({ snapshots: { enabled: false, count: 0, bytes: 0, budget_bytes: 0, max_age_seconds: 0, sweep_seconds: 0 } }));
    renderPage(<SettingsPage />);
    expect(await screen.findByText(/Every task starts cold/)).toBeTruthy();
  });

  it("lets a signed-in user make, see and revoke their personal tokens", async () => {
    const listed = [{ id: 3, name: "old laptop", created_at: "2026-11-01T10:00:00Z", last_used_at: null }];
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input.toString();
      const data =
        url === "/api/me" ? { signed_in: true, sign_in: true, login: "alice", name: "", avatar_url: "", admin: false }
        : url === "/api/tokens" && init?.method === "POST" ? { id: 4, name: "laptop MCP", created_at: "2026-11-04T10:00:00Z", last_used_at: null, token: "frt_secret" }
        : url === "/api/tokens" ? listed
        : url === "/api/tokens/3" ? {}
        : undefined;
      if (data === undefined) return new Response("not found", { status: 404 });
      return new Response(JSON.stringify({ success: true, data }), { status: 200 });
    });
    vi.stubGlobal("fetch", fetchMock);
    renderPage(<SettingsPage />);
    expect(await screen.findByText("Only an admin can see the server's settings.")).toBeTruthy();
    expect(await screen.findByText("old laptop")).toBeTruthy();
    expect(screen.getByText("never used")).toBeTruthy();

    fireEvent.change(screen.getByLabelText("Token name"), { target: { value: "laptop MCP" } });
    fireEvent.click(screen.getByRole("button", { name: /create token/ }));
    expect(await screen.findByText("frt_secret")).toBeTruthy();
    const post = fetchMock.mock.calls.find(([u, i]) => u.toString() === "/api/tokens" && i?.method === "POST");
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ name: "laptop MCP" });

    fireEvent.click(screen.getByRole("button", { name: /revoke/ }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u, i]) => u.toString() === "/api/tokens/3" && i?.method === "DELETE")).toBe(true));
  });
});
