import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import type { Task } from "../api/types";
import { ThreadPage } from "./Thread";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function task(over: Partial<Task>): Task {
  return {
    id: "t-1", repo: "https://github.com/depak7/playground", prompt: "Add a test", agent: "cursor", model: "auto",
    effort: "", thinking: false, fast: false, model_id: "auto", state: "succeeded", error: "", has_diff: true,
    snapshot_id: null, parent_id: null, thread_id: "t-1",
    pr: { requested: true, url: "https://github.com/depak7/playground/pull/5", number: 5, branch: "forgerun/add-a-test-t1", error: "" },
    summary: "", reply: "Added a **regression** test in `x_test.go`.", pushed: true, plan: false, asked: false, created_at: "2026-10-15T10:00:00Z",
    started_at: "2026-10-15T10:00:01Z", finished_at: "2026-10-15T10:02:01Z",
    ...over,
  };
}

const agents = [
  { id: "claude-code", name: "Claude Code", default_model: "sonnet", runnable: true, credential: { name: "CLAUDE_CODE_OAUTH_TOKEN", set: true, optional: false } },
  { id: "cursor", name: "Cursor", default_model: "auto", runnable: true, credential: { name: "CURSOR_API_KEY", set: true, optional: false } },
];
const models = (agent: string) => ({
  agent, runnable: true, default: agent === "cursor" ? "auto" : "sonnet",
  models: agent === "cursor"
    ? [{ id: "auto", name: "Auto", efforts: [], default_effort: "", thinking: false, fast: false, zdr: true, keyless: false }]
    : [{ id: "sonnet", name: "Sonnet", efforts: [], default_effort: "", thinking: false, fast: false, zdr: false, keyless: false }],
});
const patch = "diff --git a/x_test.go b/x_test.go\n--- a/x_test.go\n+++ b/x_test.go\n@@ -1,1 +1,2 @@\n package x\n+// new test\n";

// server answers by path and records POSTs.
function server(steps: Task[], session = { open: true, busy: false, idle_until: "2026-10-16T12:30:00Z" }) {
  const posted: { url: string; body: unknown }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input.toString();
      const ok = (data: unknown) => new Response(JSON.stringify({ success: true, data }), { status: 200 });
      if (init?.method === "POST") {
        posted.push({ url, body: init.body ? JSON.parse(String(init.body)) : null });
        return ok(task({ id: "t-new", state: "queued" }));
      }
      if (url.endsWith("/thread")) return ok(steps);
      if (url.endsWith("/session")) return ok(session);
      if (url.endsWith("/diff")) return new Response(patch, { status: 200 });
      if (url.endsWith("/log")) return new Response(`==> cloning\nlog of ${url.split("/")[3]}\n`, { status: 200 });
      if (url.startsWith("/api/agents")) return ok(agents);
      if (url.startsWith("/api/models?agent=")) return ok(models(url.split("=")[1]));
      return new Response("", { status: 404 });
    }),
  );
  return posted;
}

function renderThread() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/threads/t-1"]}>
        <Routes>
          <Route path="/threads/:id" element={<ThreadPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const followup = () => screen.getByLabelText("Follow up on this thread") as HTMLTextAreaElement;
const sendButton = () => screen.getByRole("button", { name: /send/ }) as HTMLButtonElement;

describe("ThreadPage", () => {
  it("shows the newest step's output and switches when another step is picked", async () => {
    server([
      task({}),
      task({ id: "t-2", parent_id: "t-1", prompt: "Also cover nil", agent: "claude-code", model: "sonnet", has_diff: false, summary: "", snapshot_id: 3 }),
    ]);
    renderThread();
    expect(await screen.findByRole("heading", { name: "Add a test" })).toBeTruthy();
    expect(screen.getByText("cursor → claude-code")).toBeTruthy();
    expect(screen.getAllByText("Added a regression test in x_test.go.").length).toBeGreaterThan(0); // step 1's reply, as plain text
    expect(await screen.findByText("log of t-2")).toBeTruthy(); // the newest step is selected

    fireEvent.click(screen.getByRole("button", { name: "Show step 1 output" }));
    expect(await screen.findByText("log of t-1")).toBeTruthy();
    fireEvent.click(screen.getByRole("tab", { name: "diff" }));
    expect(await screen.findByText("+// new test")).toBeTruthy();
    expect(screen.getByRole("link", { name: /branch ↗/ }).getAttribute("href")).toBe("https://github.com/depak7/playground/tree/forgerun/add-a-test-t1");
  });

  it("closes the follow-up box and offers cancel while a step runs", async () => {
    const posted = server([task({}), task({ id: "t-2", parent_id: "t-1", state: "running", finished_at: null, has_diff: false, summary: "" })]);
    renderThread();
    expect(await screen.findByText(/Step 2 is running on cursor/)).toBeTruthy();
    expect(screen.queryByLabelText("Follow up on this thread")).toBeNull();
    expect(screen.getByText("FOLLOWING LIVE OUTPUT")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /cancel step 2/ }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0].url).toBe("/api/tasks/t-2/cancel");
  });

  it("shows the open session and pushes unpushed changes with a pull request", async () => {
    const posted = server([
      task({ pr: { requested: false, url: "", number: 0, branch: "", error: "" }, pushed: false }),
      task({ id: "t-2", parent_id: "t-1", prompt: "Also cover nil", pushed: false, pr: { requested: false, url: "", number: 0, branch: "", error: "" } }),
    ]);
    renderThread();
    expect(await screen.findByText(/session open · closes/)).toBeTruthy();
    expect(screen.getByText("2 unpushed")).toBeTruthy();
    expect(screen.getAllByText("NOT PUSHED YET")).toHaveLength(2);
    expect(screen.getByText(/continues the agent's conversation in its open session/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /push & open PR/ }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toEqual({ url: "/api/threads/t-1/push", body: { pr: true } });

    fireEvent.click(screen.getByRole("button", { name: /end session/ }));
    await waitFor(() => expect(posted).toHaveLength(2));
    expect(posted[1].url).toBe("/api/threads/t-1/end");
  });

  it("offers only push to the PR once there is one, and says a closed session reopens", async () => {
    server([task({ pushed: false })], { open: false, busy: false, idle_until: null as unknown as string });
    renderThread();
    expect(await screen.findByRole("button", { name: /push to PR #5/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /open PR/ })).toBeNull();
    expect(await screen.findByText("session closed")).toBeTruthy();
    expect(screen.getByText(/reopens the session/)).toBeTruthy();
  });

  it("shows a plan in full and approves it, or revises it as plan-only", async () => {
    const plan = "## Plan\n- Add is_palindrome to script.sh\n## Questions\n- Strict or lenient?";
    const posted = server([task({ plan: true, has_diff: false, pushed: false, reply: plan, pr: { requested: false, url: "", number: 0, branch: "", error: "" } })]);
    renderThread();
    expect(await screen.findByText("Here's the plan:")).toBeTruthy();
    expect(screen.getByText("Add is_palindrome to script.sh")).toBeTruthy();
    expect(screen.getByText("PLAN")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /approve & build/ }));
    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toEqual({ url: "/api/tasks/t-1/followups", body: { prompt: "Approved. Go ahead and implement the plan.", agent: "cursor", model: "auto" } });

    fireEvent.click(screen.getByRole("button", { name: /revise/ }));
    expect((screen.getByLabelText(/plan only/) as HTMLInputElement).checked).toBe(true);
    expect(followup().placeholder).toBe("What should change in the plan?");
    fireEvent.change(followup(), { target: { value: "Use Go instead" } });
    await waitFor(() => expect(sendButton().disabled).toBe(false));
    fireEvent.click(sendButton());
    await waitFor(() => expect(posted).toHaveLength(2));
    expect(posted[1].body).toEqual({ prompt: "Use Go instead", agent: "cursor", model: "auto", plan: true });
  });

  it("asks for answers when the agent has questions", async () => {
    server([task({ asked: true, has_diff: false, pushed: false, reply: "## Questions\n1. Which file?" })]);
    renderThread();
    expect(await screen.findByText("NEEDS YOUR ANSWER")).toBeTruthy();
    expect(screen.getByText("Before going on, the agent asks:")).toBeTruthy();
    expect(screen.getByText(/Which file\?/)).toBeTruthy();
    expect(followup().placeholder).toBe("Answer the agent's questions…");
    expect(screen.queryByRole("button", { name: /approve/ })).toBeNull();
  });

  it("retries a failed step and sends the follow-up with the chosen agent", async () => {
    const posted = server([
      task({}),
      task({ id: "t-2", parent_id: "t-1", prompt: "Also cover nil", state: "failed", error: "agent exited 1", has_diff: false, reply: "" }),
    ]);
    renderThread();
    expect(await screen.findByText("agent exited 1")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /retry as follow-up/ }));
    expect(followup().value).toBe("Also cover nil");

    fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "claude-code" } });
    await waitFor(() => expect((screen.getByLabelText("Model") as HTMLSelectElement).value).toBe("sonnet"));
    await waitFor(() => expect(sendButton().disabled).toBe(false));
    fireEvent.click(sendButton());

    await waitFor(() => expect(posted).toHaveLength(1));
    expect(posted[0]).toEqual({ url: "/api/tasks/t-2/followups", body: { prompt: "Also cover nil", agent: "claude-code", model: "sonnet" } });
    await waitFor(() => expect(followup().value).toBe(""));
  });
});
