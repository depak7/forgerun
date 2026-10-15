import { describe, expect, it } from "vitest";
import { formatBytes, formatDuration, formatElapsed, formatWhen, shortID } from "./format";

describe("format", () => {
  it("formats sizes", () => {
    expect(formatBytes(26574848)).toBe("26.6 MB");
    expect(formatBytes(20e9)).toBe("20 GB");
    expect(formatBytes(512)).toBe("512 B");
  });
  it("formats durations", () => {
    expect(formatElapsed(45_000)).toBe("45s");
    expect(formatElapsed(134_000)).toBe("2m 14s");
    expect(formatElapsed(3_780_000)).toBe("1h 3m");
  });
  it("formats configured lengths of time", () => {
    expect(formatDuration(604800)).toBe("7 days");
    expect(formatDuration(86400)).toBe("1 day");
    expect(formatDuration(129600)).toBe("36 hours");
    expect(formatDuration(600)).toBe("10 min");
    expect(formatDuration(45)).toBe("45s");
  });
  it("formats times relative to now", () => {
    const now = new Date(2026, 9, 15, 14, 0, 0);
    expect(formatWhen(new Date(2026, 9, 15, 13, 59, 30).toISOString(), now)).toBe("now");
    expect(formatWhen(new Date(2026, 9, 15, 13, 44).toISOString(), now)).toBe("13:44");
    expect(formatWhen(new Date(2026, 9, 14, 9, 0).toISOString(), now)).toBe("yesterday");
  });
  it("shortens task ids", () => {
    expect(shortID("t-20261015-134423-629e08")).toBe("t-…629e08");
  });
});

describe("display names", () => {
  it("shortens repo URLs", async () => {
    const { repoName } = await import("./format");
    expect(repoName("https://github.com/depak7/playground")).toBe("depak7/playground");
    expect(repoName("https://github.com/depak7/playground.git/")).toBe("depak7/playground");
    expect(repoName("https://gitlab.com/a/b")).toBe("gitlab.com/a/b");
  });
  it("collapses repeated agents", async () => {
    const { agentChain } = await import("./format");
    expect(agentChain(["cursor", "claude-code", "cursor"])).toBe("cursor → claude-code → cursor");
    expect(agentChain(["cursor", "cursor", "cursor", "cursor", "cursor", "cursor"])).toBe("cursor ×6");
    expect(agentChain(["cursor", "cursor", "claude-code"])).toBe("cursor ×2 → claude-code");
  });
});

describe("repo checks", () => {
  it("normalizes like the server", async () => {
    const { normalizeRepo } = await import("./format");
    expect(normalizeRepo("  HTTPS://GitHub.com/depak7/Playground.git/ ")).toBe("https://github.com/depak7/Playground");
  });
  it("explains bad URLs", async () => {
    const { checkRepo, isGitHub } = await import("./format");
    expect(checkRepo("")).toMatch(/Enter/);
    expect(checkRepo("git@github.com:a/b.git")).toMatch(/https URL/);
    expect(checkRepo("https://github.com/a/b")).toBe("");
    expect(isGitHub("https://github.com/a/b")).toBe(true);
    expect(isGitHub("https://gitlab.com/a/b")).toBe(false);
  });
});
