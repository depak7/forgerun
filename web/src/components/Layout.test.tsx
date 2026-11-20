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
  sessions: { open: 0, max: 4, idle_seconds: 1800 },
};
const me = (over: object) => ({ signed_in: true, sign_in: true, login: "owner", ...over });

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
  it("asks for the username and password when signed out", async () => {
    fakeServer({ "/api/me": me({ signed_in: false, login: "" }), "/api/status": status, "/api/threads": [], "/api/agents": [] });
    renderLayout();
    expect(await screen.findByLabelText("Password")).toBeTruthy();
    expect(screen.getByRole("button", { name: /sign in/ })).toBeTruthy();
    expect(screen.queryByText("the page")).toBeNull();
  });

  it("shows who's signed in, the settings and the server's credentials", async () => {
    fakeServer({ "/api/me": me({}), "/api/status": status, "/api/threads": [], "/api/agents": [] });
    renderLayout();
    expect(await screen.findByText("the page")).toBeTruthy();
    expect(screen.getByText("owner")).toBeTruthy();
    expect(screen.getByRole("button", { name: "sign out" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "settings" })).toBeTruthy();
    expect(screen.getByText("Credentials")).toBeTruthy();
  });

  it("has no account row in local mode", async () => {
    fakeServer({ "/api/me": me({ sign_in: false, login: "local" }), "/api/status": status, "/api/threads": [], "/api/agents": [] });
    renderLayout();
    expect(await screen.findByText("the page")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "sign out" })).toBeNull();
  });
});
