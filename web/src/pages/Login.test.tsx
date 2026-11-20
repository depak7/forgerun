import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderPage } from "../test-utils";
import { LoginPage } from "./Login";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("LoginPage", () => {
  it("sends the username and password, and shows why a sign-in failed", async () => {
    const fetchMock = vi.fn(
      async () => new Response(JSON.stringify({ success: false, error: { code: "unauthorized", message: "wrong username or password" } }), { status: 401 }),
    );
    vi.stubGlobal("fetch", fetchMock);
    renderPage(<LoginPage />);
    const button = screen.getByRole("button", { name: /sign in/ }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Username"), { target: { value: "owner" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "nope" } });
    fireEvent.click(button);
    expect((await screen.findByRole("alert")).textContent).toBe("wrong username or password");
    await waitFor(() => expect(fetchMock).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/auth/login");
    expect(JSON.parse(String(init.body))).toEqual({ username: "owner", password: "nope" });
  });
});
