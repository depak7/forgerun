import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";
import { vi } from "vitest";

// fakeServer answers fetch by path: JSON values become a success envelope,
// strings are sent as plain text (logs).
export function fakeServer(routes: Record<string, unknown>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === "string" ? input : input.toString();
      const path = url.split("?")[0];
      if (!(path in routes)) return new Response("not found", { status: 404 });
      const body = routes[path];
      if (typeof body === "string") return new Response(body, { status: 206 });
      return new Response(JSON.stringify({ success: true, data: body }), { status: 200 });
    }),
  );
}

export function renderPage(ui: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}
