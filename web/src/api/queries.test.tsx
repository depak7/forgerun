import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useLiveLog } from "./queries";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

// logServer serves a growing log the way forgerun does: the whole file, or
// from a byte offset (206), or 416 when the offset is past the end.
function logServer() {
  let log = new Uint8Array();
  const statuses: number[] = [];
  const append = (b: Uint8Array) => {
    const next = new Uint8Array(log.length + b.length);
    next.set(log);
    next.set(b, log.length);
    log = next;
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url: string, init?: RequestInit) => {
      const range = (init?.headers as Record<string, string> | undefined)?.Range;
      let res: Response;
      if (!range) {
        res = new Response(log.slice(), { status: 200 });
      } else {
        const from = Number(range.match(/^bytes=(\d+)-$/)![1]);
        res = from >= log.length ? new Response(null, { status: 416 }) : new Response(log.slice(from), { status: 206 });
      }
      statuses.push(res.status);
      return res;
    }),
  );
  return { append, statuses };
}

const poll = () => act(() => vi.advanceTimersByTimeAsync(2000));

describe("useLiveLog", () => {
  it("follows a growing log without 416s while it's quiet", async () => {
    vi.useFakeTimers();
    const server = logServer();
    const enc = new TextEncoder();
    server.append(enc.encode("==> cloning\n"));

    const { result } = renderHook(() => useLiveLog("t-1", true));
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(result.current.text).toBe("==> cloning\n");

    await poll(); // nothing new
    await poll();
    expect(result.current.text).toBe("==> cloning\n");

    // "é" is two bytes: the first arrives in one poll, the second in the next.
    const e = enc.encode("café\n");
    server.append(e.subarray(0, 4));
    await poll();
    server.append(e.subarray(4));
    await poll();
    expect(result.current.text).toBe("==> cloning\ncafé\n");
    expect(server.statuses).not.toContain(416);
  });
});
