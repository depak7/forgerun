import { cleanup, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Status } from "../api/types";
import { renderPage } from "../test-utils";
import { SettingsPage } from "./Settings";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const agents = [
  { id: "claude-code", name: "Claude Code", default_model: "sonnet", runnable: true, credential: { name: "CLAUDE_CODE_OAUTH_TOKEN", set: true } },
  { id: "cursor", name: "Cursor", default_model: "auto", runnable: true, credential: { name: "CURSOR_API_KEY", set: false } },
];
const claudeModels = {
  agent: "claude-code", runnable: true, default: "sonnet",
  models: [
    { id: "sonnet", name: "Sonnet", efforts: ["low", "medium", "high"], default_effort: "medium", thinking: true, fast: false, zdr: true },
    { id: "haiku", name: "Haiku", efforts: [], default_effort: "", thinking: false, fast: true, zdr: false },
  ],
};
const cursorModels = { agent: "cursor", runnable: true, default: "auto", models: [{ id: "auto", name: "Auto", efforts: [], default_effort: "", thinking: false, fast: false, zdr: true }] };

function status(over: Partial<Status> = {}): Status {
  return {
    pull_requests: { enabled: true, github_user: "depak7" },
    snapshots: { enabled: true, count: 1, bytes: 5e9, budget_bytes: 20e9, max_age_seconds: 604800, sweep_seconds: 600 },
    tasks: { queued: 0, running: 0 },
    require_zdr: false,
    sessions: { open: 1, max: 4, idle_seconds: 1800 },
    ...over,
  };
}
const snapshots = [
  { id: 1, repo: "github.com/depak7/playground", agent: "cursor", commit: "abcdef1234", state: "current", size_bytes: 5e9, created_at: "2026-10-14T10:00:00Z", last_used_at: "2026-10-14T10:00:00Z" },
];

// serve answers the page's requests; the model list depends on ?agent=.
function serve(st: Status) {
  const routes: Record<string, unknown> = {
    "/api/agents": agents,
    "/api/status": st,
    "/api/snapshots": snapshots,
    "/api/models?agent=claude-code": claudeModels,
    "/api/models?agent=cursor": cursorModels,
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

  it("says when snapshots are off", async () => {
    serve(status({ snapshots: { enabled: false, count: 0, bytes: 0, budget_bytes: 0, max_age_seconds: 0, sweep_seconds: 0 } }));
    renderPage(<SettingsPage />);
    expect(await screen.findByText(/Every task starts cold/)).toBeTruthy();
  });
});
