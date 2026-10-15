// A small client for forgerun's JSON API. Every response is an envelope:
//   {"success": true,  "data": …}
//   {"success": false, "error": {"code": "…", "message": "…"}}
// api() unwraps it: it returns data, or throws an ApiError.

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

type Envelope<T> =
  | { success: true; data: T }
  | { success: false; error: { code: string; message: string } };

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { Accept: "application/json", ...(init?.body ? { "Content-Type": "application/json" } : {}), ...init?.headers },
  });
  let body: Envelope<T>;
  try {
    body = (await res.json()) as Envelope<T>;
  } catch {
    // Not our JSON at all: a proxy error page, or the server is down.
    throw new ApiError(res.status, "bad_response", `The server answered ${res.status} without a forgerun response.`);
  }
  if (!body.success) {
    throw new ApiError(res.status, body.error.code, body.error.message);
  }
  return body.data;
}
