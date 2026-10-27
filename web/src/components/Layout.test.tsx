import { cleanup, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Route, Routes } from "react-router-dom";
import { fakeServer, renderPage } from "../test-utils";
import { Layout } from "./Layout";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const status = {
  pull_requests: { enabled: true, github_user: "depak7" },
  snapshots: { enabled: false, count: 0, bytes: 0, budget_bytes: 0, max_age_seconds: 0, sweep_seconds: 0 },
  tasks: { queued: 0, running: 0 },
  require_zdr: false,
  remote_workers: false,
  paused: false,
  limits: { turns_per_day: 20, live: 1 },
  sessions: { open: 0, max: 4, idle_seconds: 1800 },
};
const me = (over: object) => ({ signed_in: true, sign_in: true, login: "alice", name: "Alice", avatar_url: "", admin: false, ...over });

function renderLayout() {
  renderPage(
    <Routes>
      <Route element={<Layout />}>
        <Route path="*" element={<p>the page</p>} />
      </Route>
    </Routes>,
  );
}

describe("Layout", () => {
  it("asks to sign in with GitHub when signed out", async () => {
    fakeServer({ "/api/me": me({ signed_in: false, login: "" }), "/api/status": status, "/api/threads": [], "/api/agents": [] });
    renderLayout();
    const button = await screen.findByRole("link", { name: /sign in with GitHub/ });
    expect(button.getAttribute("href")).toBe("/auth/github/login");
    expect(screen.queryByText("the page")).toBeNull();
  });

  it("leaves the sign-in buttons off when the server hides them", async () => {
    fakeServer({ "/api/me": me({ signed_in: false, login: "", sign_in_hidden: true }), "/api/status": status, "/api/threads": [], "/api/agents": [] });
    renderLayout();
    expect(await screen.findByRole("link", { name: /view on GitHub/ })).toBeTruthy();
    expect(screen.queryByRole("link", { name: /sign in/ })).toBeNull();
  });

  it("shows the signed-in user, without settings unless they're an admin", async () => {
    fakeServer({ "/api/me": me({}), "/api/status": status, "/api/threads": [], "/api/agents": [] });
    renderLayout();
    expect(await screen.findByText("the page")).toBeTruthy();
    expect(screen.getByText("alice")).toBeTruthy();
    expect(screen.getByRole("button", { name: "sign out" })).toBeTruthy();
    expect(screen.queryByRole("link", { name: "settings" })).toBeNull();
    expect(screen.queryByText("Credentials")).toBeNull();
  });

  it("gives an admin the settings", async () => {
    fakeServer({ "/api/me": me({ admin: true }), "/api/status": status, "/api/threads": [], "/api/agents": [] });
    renderLayout();
    expect(await screen.findByRole("link", { name: "settings" })).toBeTruthy();
    expect(screen.getByText("admin")).toBeTruthy();
  });

  it("has no account row in local mode", async () => {
    fakeServer({ "/api/me": me({ sign_in: false, login: "local", admin: true }), "/api/status": status, "/api/threads": [], "/api/agents": [] });
    renderLayout();
    expect(await screen.findByText("the page")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "sign out" })).toBeNull();
  });
});
