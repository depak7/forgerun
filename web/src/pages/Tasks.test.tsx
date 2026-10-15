import { cleanup, fireEvent, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Task, Thread } from "../api/types";
import { fakeServer, renderPage } from "../test-utils";
import { TasksPage } from "./Tasks";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const status = {
  pull_requests: { enabled: true, github_user: "depak7" },
  snapshots: { enabled: true, count: 2, bytes: 26574848, budget_bytes: 20e9 },
  tasks: { queued: 0, running: 1 },
};
const agents = [
  { id: "claude-code", name: "Claude Code", default_model: "sonnet", runnable: true, credential: { name: "CLAUDE_CODE_OAUTH_TOKEN", set: false } },
  { id: "cursor", name: "Cursor", default_model: "auto", runnable: true, credential: { name: "CURSOR_API_KEY", set: true } },
];

function task(over: Partial<Task>): Task {
  return {
    id: "t-20261015-000000-aaaaaa", repo: "https://github.com/depak7/playground", prompt: "p", agent: "cursor", model: "auto",
    effort: "", thinking: false, fast: false, model_id: "auto", state: "succeeded", error: "", has_diff: true, snapshot_id: null,
    parent_id: null, thread_id: "t-20261015-000000-aaaaaa", pr: { requested: false, url: "", number: 0, branch: "", error: "" },
    summary: "", reply: "", pushed: false, plan: false, asked: false, created_at: new Date().toISOString(), started_at: null, finished_at: null, ...over,
  };
}

const threads: Thread[] = [
  {
    id: "t-1", title: "Add a CONTRIBUTING.md", repo: "https://github.com/depak7/playground", steps: 3, agents: ["cursor", "claude-code", "cursor"],
    needs_attention: false, session_open: false, updated_at: new Date().toISOString(),
    latest: task({ id: "t-20261015-140200-6a2c11", prompt: "Add a reporting contact", state: "running", started_at: new Date(Date.now() - 134_000).toISOString(),
      pr: { requested: true, url: "u", number: 5, branch: "b", error: "" } }),
  },
  {
    id: "t-2", title: "Document the sweep flags", repo: "https://github.com/depak7/forgerun", steps: 1, agents: ["claude-code"],
    needs_attention: true, session_open: false, updated_at: new Date().toISOString(),
    latest: task({ agent: "claude-code", pr: { requested: true, url: "", number: 0, branch: "", error: "the token needs: contents=write" } }),
  },
  {
    id: "t-3", title: "Upgrade the test runner", repo: "https://github.com/depak7/playground", steps: 1, agents: ["cursor"],
    needs_attention: true, session_open: false, updated_at: new Date().toISOString(),
    latest: task({ state: "failed", error: "agent stalled: no output or file changes for 10m" }),
  },
];

describe("TasksPage", () => {
  it("shows the first-run guide when there are no tasks", async () => {
    fakeServer({ "/api/threads": [], "/api/status": status, "/api/agents": agents });
    renderPage(<TasksPage />);
    expect(await screen.findByText("Give an agent its first task")).toBeTruthy();
    expect(await screen.findByText(/cursor ready/)).toBeTruthy();
    expect(screen.getByText("CLAUDE_CODE_OAUTH_TOKEN")).toBeTruthy();
    expect(await screen.findByText(/connected as depak7/)).toBeTruthy();
    expect(screen.queryByText("Threads")).toBeNull();
  });

  it("shows what's running now, with the agent's last line", async () => {
    fakeServer({ "/api/threads": threads, "/api/status": status, "/api/agents": agents,
      "/api/tasks/t-20261015-140200-6a2c11/log": "…ing CONTRIBUTING.md\nLinking the Contributor Covenant 2.1\n" });
    renderPage(<TasksPage />);
    const now = await screen.findByRole("region", { name: "Running and queued" });
    expect(within(now).getByText("Add a reporting contact")).toBeTruthy();
    expect(await within(now).findByText("Linking the Contributor Covenant 2.1")).toBeTruthy();
    expect(within(now).getByText(/step 3 of PR #5/)).toBeTruthy();
    expect(within(now).getByText("2m 14s")).toBeTruthy();
  });

  it("flags threads that need attention and filters to them", async () => {
    fakeServer({ "/api/threads": threads, "/api/status": status, "/api/agents": agents });
    renderPage(<TasksPage />);
    const list = await screen.findByRole("region", { name: "Threads" });
    expect(within(list).getByText("PUSH FAILED")).toBeTruthy();
    expect(within(list).getByText(/the token needs: contents=write/)).toBeTruthy();
    expect(within(list).getByText("cursor → claude-code → cursor")).toBeTruthy();

    fireEvent.click(within(list).getByRole("button", { name: /NEEDS ATTENTION · 2/ }));
    expect(within(list).queryByText("Add a CONTRIBUTING.md")).toBeNull();
    expect(within(list).getByText("Document the sweep flags")).toBeTruthy();
    expect(within(list).getByText("Upgrade the test runner")).toBeTruthy();
  });

  it("searches titles and repos", async () => {
    fakeServer({ "/api/threads": threads, "/api/status": status, "/api/agents": agents });
    renderPage(<TasksPage />);
    const list = await screen.findByRole("region", { name: "Threads" });
    fireEvent.change(within(list).getByLabelText("Search threads"), { target: { value: "forgerun" } });
    expect(within(list).getByText("Document the sweep flags")).toBeTruthy();
    expect(within(list).queryByText("Upgrade the test runner")).toBeNull();
    fireEvent.change(within(list).getByLabelText("Search threads"), { target: { value: "nothing like this" } });
    expect(within(list).getByText("No threads match.")).toBeTruthy();
  });
});
