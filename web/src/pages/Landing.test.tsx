import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LandingPage } from "./Landing";

afterEach(() => {
  cleanup();
});

describe("LandingPage", () => {
  it("says what forgerun is, with no way to sign in", () => {
    render(<LandingPage />);
    expect(screen.getByRole("heading", { level: 1 }).textContent).toMatch(/your own VM/);
    expect(screen.getByText(/every turn in its own sandbox/)).toBeTruthy();
    expect(screen.queryByText(/sign in/i)).toBeNull();
    expect(screen.getByRole("link", { name: /view on GitHub/ }).getAttribute("href")).toBe("https://github.com/depak7/forgerun");
    expect(screen.getByRole("link", { name: "Star on GitHub" }).getAttribute("href")).toBe("https://github.com/depak7/forgerun");
    expect(screen.getByRole("link", { name: /\[ star on GitHub \]/ }).getAttribute("href")).toBe("https://github.com/depak7/forgerun");
    for (const section of [
      "How it works",
      "Why it's safe to point at your code",
      "Agents",
      "How it compares",
      "Run it yourself",
      "Frequently asked questions",
    ]) {
      expect(screen.getByRole("heading", { name: section })).toBeTruthy();
    }
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
    // The browser's own controls pause and replay it; it plays once, not on a loop.
    expect(video?.controls).toBe(true);
    expect(video?.loop).toBe(false);
  });

  it("starts the demo with sound, or muted when the browser won't allow sound", async () => {
    const play = vi
      .spyOn(HTMLMediaElement.prototype, "play")
      .mockRejectedValueOnce(new DOMException("needs a click first", "NotAllowedError"))
      .mockResolvedValue(undefined);
    render(<LandingPage />);
    const video = screen.getByRole("region", { name: "Demo video" }).querySelector("video")!;
    await vi.waitFor(() => expect(play).toHaveBeenCalledTimes(2));
    expect(video.muted).toBe(true);
    play.mockRestore();
  });
});
