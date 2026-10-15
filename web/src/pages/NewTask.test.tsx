import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { NewTaskPage } from "./NewTask";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const agents = [
  { id: "claude-code", name: "Claude Code", default_model: "sonnet", runnable: true, credential: { name: "CLAUDE_CODE_OAUTH_TOKEN", set: false } },
  { id: "cursor", name: "Cursor", default_model: "auto", runnable: true, credential: { name: "CURSOR_API_KEY", set: true } },
];
const cursorModels = {
  agent: "cursor", runnable: true, default: "auto",
  models: [
    { id: "auto", name: "Auto", efforts: [], default_effort: "", thinking: false, fast: false, zdr: true },
    { id: "claude-opus-5-5", name: "Claude Opus 5.5 1M", efforts: ["medium", "high"], default_effort: "medium", thinking: false, fast: true, zdr: true },
  ],
};
const status = { pull_requests: { enabled: true, github_user: "depak7" }, snapshots: { enabled: true, count: 1, bytes: 1, budget_bytes: 0 }, tasks: { queued: 0, running: 0 } };
const snapshots = [{ id: 1, repo: "https://github.com/depak7/playground", agent: "cursor", commit: "c", state: "current", size_bytes: 1, created_at: "", last_used_at: "" }];

// server answers GETs by path and records the POST body.
function server() {
  const posted: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = input.toString();
      const ok = (data: unknown) => new Response(JSON.stringify({ success: true, data }), { status: 200 });
      if (init?.method === "POST") {
        posted.push(JSON.parse(String(init.body)));
        return ok({ id: "t-new", thread_id: "t-new" });
      }
      if (url.startsWith("/api/agents")) return ok(agents);
      if (url.startsWith("/api/models?agent=cursor")) return ok(cursorModels);
      if (url.startsWith("/api/models")) return ok({ agent: "claude-code", runnable: true, default: "sonnet", models: [{ id: "sonnet", name: "Sonnet", efforts: ["low", "high"], default_effort: "", thinking: false, fast: false, zdr: false }] });
      if (url.startsWith("/api/status")) return ok(status);
      if (url.startsWith("/api/snapshots")) return ok(snapshots);
      if (url.startsWith("/api/threads")) return ok([]);
      return new Response("", { status: 404 });
    }),
  );
  return posted;
}

function renderNew(path = "/tasks/new") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/tasks/new" element={<NewTaskPage />} />
          <Route path="/threads/:id" element={<p>thread page</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const repoInput = () => screen.getByLabelText("repo") as HTMLInputElement;
const runButton = () => screen.getByRole("button", { name: /run task/ }) as HTMLButtonElement;

describe("NewTaskPage", () => {
  it("defaults to an agent with a key, and explains a missing one", async () => {
    server();
    renderNew("/tasks/new?prompt=Add%20a%20test&repo=https://github.com/depak7/playground");
    await waitFor(() => expect(screen.getByRole("button", { name: /cursor/, pressed: true })).toBeTruthy());
    expect(screen.getByText("KEY MISSING")).toBeTruthy();
    await waitFor(() => expect(runButton().disabled).toBe(false));

    fireEvent.click(screen.getByRole("button", { name: /claude-code/ }));
    expect(await screen.findByText(/claude-code has no credential on this server: set CLAUDE_CODE_OAUTH_TOKEN/)).toBeTruthy();
    expect(runButton().disabled).toBe(true);
  });

  it("checks the repo as you type and needs a GitHub repo to push to", async () => {
    server();
    renderNew("/tasks/new?prompt=x&repo=https://github.com/depak7/playground");
    expect(await screen.findByText("● WARM SNAPSHOT")).toBeTruthy();
    expect(screen.getByText(/Nothing is pushed until you press push/)).toBeTruthy();

    fireEvent.change(repoInput(), { target: { value: "git@github.com:depak7/playground.git" } });
    expect(screen.getAllByText(/Use an https URL/).length).toBeGreaterThan(0);
    expect(runButton().disabled).toBe(true);

    fireEvent.change(repoInput(), { target: { value: "https://gitlab.com/depak7/playground" } });
    expect(screen.getByText(/Needs a github.com repo/)).toBeTruthy();
    expect(screen.getByText(/the repo must be on github.com/)).toBeTruthy();
    expect(runButton().disabled).toBe(true);
    expect(screen.getByText("COLD START")).toBeTruthy();
  });

  it("sends the chosen agent, model and effort, then opens the thread", async () => {
    const posted = server();
    renderNew("/tasks/new?prompt=Add%20a%20test&repo=https://github.com/depak7/playground");
    const model = (await screen.findByLabelText("Model")) as HTMLSelectElement;
    await waitFor(() => expect(model.value).toBe("auto"));
    fireEvent.change(model, { target: { value: "claude-opus-5-5" } });
    fireEvent.click(await screen.findByRole("button", { name: "high" }));
    fireEvent.click(screen.getByLabelText(/fast/));
    await waitFor(() => expect(runButton().disabled).toBe(false));
    fireEvent.click(runButton());

    expect(await screen.findByText("thread page")).toBeTruthy();
    expect(posted).toEqual([
      { repo: "https://github.com/depak7/playground", prompt: "Add a test", agent: "cursor", model: "claude-opus-5-5", effort: "high", fast: true },
    ]);
  });

  it("sends plan: true when Plan first is on", async () => {
    const posted = server();
    renderNew("/tasks/new?prompt=Add%20a%20test&repo=https://github.com/depak7/playground");
    const model = (await screen.findByLabelText("Model")) as HTMLSelectElement;
    await waitFor(() => expect(model.value).toBe("auto"));
    fireEvent.click(screen.getByRole("switch", { name: "Plan first" }));
    await waitFor(() => expect(runButton().disabled).toBe(false));
    fireEvent.click(runButton());
    expect(await screen.findByText("thread page")).toBeTruthy();
    expect(posted).toEqual([{ repo: "https://github.com/depak7/playground", prompt: "Add a test", agent: "cursor", model: "auto", plan: true }]);
  });

  it("asks for a prompt before running", async () => {
    server();
    renderNew("/tasks/new?repo=https://github.com/depak7/playground");
    expect(await screen.findByText("Say what the agent should do.")).toBeTruthy();
  });
});
