import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LandingPage } from "./Landing";

afterEach(() => {
  cleanup();
  window.history.replaceState(null, "", "/");
});

describe("LandingPage", () => {
  it("says what forgerun is and how to sign in", () => {
    render(<LandingPage />);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toMatch(/your own VM/);
    expect(screen.getByText(/every turn in its own sandbox/)).toBeTruthy();
    expect(screen.getByRole("link", { name: /sign in with GitHub/ }).getAttribute("href")).toBe("/auth/github/login");
    expect(screen.getByRole("link", { name: /view on GitHub/ }).getAttribute("href")).toBe("https://github.com/depak7/forgerun");
    for (const section of ["How it works", "Why it's safe to point at your code", "Agents", "What signing in means", "Run it yourself"]) {
      expect(screen.getByRole("heading", { name: section })).toBeTruthy();
    }
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows the one-line install and copies it", async () => {
    const writeText = vi.fn(async () => {});
    Object.assign(navigator, { clipboard: { writeText } });
    render(<LandingPage />);
    const cmd = "curl -fsSL https://raw.githubusercontent.com/depak7/forgerun/main/install.sh | sh";
    expect(screen.getByText(cmd)).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Everything here" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "A worker" })).toBeTruthy();
    expect(screen.getByText("forgerun doctor")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "[ copy ]" }));
    expect(writeText).toHaveBeenCalledWith(cmd);
    expect(await screen.findByRole("button", { name: "[ copied ]" })).toBeTruthy();
  });

  it("puts try-it-yourself in the hero, then the demo", () => {
    render(<LandingPage />);
    const tryIt = screen.getByRole("group", { name: /TRY IT YOURSELF/ });
    expect(tryIt.textContent).toContain("install.sh | sh");
    const video = screen.getByRole("region", { name: "Demo video" }).querySelector("video");
    expect(video?.getAttribute("src")).toBe("/demo/forgerun-demo.mp4");
    // The video starts muted (so it may autoplay); the button turns the sound on.
    expect(video?.muted).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "[ sound on ]" }));
    expect(video?.muted).toBe(false);
    expect(screen.getByRole("button", { name: "[ sound off ]" })).toBeTruthy();
  });

  it("explains a failed sign-in", () => {
    window.history.replaceState(null, "", "/?signin_error=access%20denied");
    render(<LandingPage />);
    expect(screen.getByRole("alert").textContent).toBe("Sign-in didn't work: access denied");
  });
});
