import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Summary } from "./summary";

afterEach(cleanup);

describe("Summary", () => {
  it("renders headings, bullets and code, never raw HTML", () => {
    const { container } = render(
      <Summary text={"## Requested\n- Add `greet(name)` to app.js\n- Keep README unchanged\n\n## Open\nNothing <script>alert(1)</script> here."} />,
    );
    expect(screen.getByText("## REQUESTED")).toBeTruthy();
    expect(container.querySelectorAll("li")).toHaveLength(2);
    expect(container.querySelector("code")?.textContent).toBe("greet(name)");
    expect(container.querySelector("script")).toBeNull();
    expect(screen.getByText(/<script>alert\(1\)<\/script>/)).toBeTruthy();
  });

  it("renders bold", () => {
    const { container } = render(<Summary text={"**File:** README.md only, not **docs**."} />);
    const bold = [...container.querySelectorAll("strong")].map((b) => b.textContent);
    expect(bold).toEqual(["File:", "docs"]);
    expect(container.textContent).not.toContain("**");
  });
});
