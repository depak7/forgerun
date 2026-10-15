import { afterEach, describe, expect, it, vi } from "vitest";
import { api, ApiError } from "./client";

function respond(status: number, body: unknown) {
  vi.stubGlobal("fetch", vi.fn(async () => new Response(typeof body === "string" ? body : JSON.stringify(body), { status })));
}

afterEach(() => vi.unstubAllGlobals());

describe("api", () => {
  it("returns data from a success envelope", async () => {
    respond(200, { success: true, data: { tasks: { queued: 1, running: 2 } } });
    await expect(api("/api/status")).resolves.toEqual({ tasks: { queued: 1, running: 2 } });
  });

  it("throws the server's code and message", async () => {
    respond(400, { success: false, error: { code: "invalid_request", message: "prompt is required" } });
    const err = await api("/api/tasks").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 400, code: "invalid_request", message: "prompt is required" });
  });

  it("explains a response that isn't ours", async () => {
    respond(502, "<html>Bad Gateway</html>");
    await expect(api("/api/status")).rejects.toMatchObject({ code: "bad_response", status: 502 });
  });
});
