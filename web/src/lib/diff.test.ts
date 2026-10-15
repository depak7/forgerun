import { describe, expect, it } from "vitest";
import { parsePatch } from "./diff";

const patch = `diff --git a/CONTRIBUTING.md b/CONTRIBUTING.md
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/CONTRIBUTING.md
@@ -0,0 +1,2 @@
+# Contributing
+Keep pull requests small.
diff --git a/README.md b/README.md
index 2222222..3333333 100644
--- a/README.md
+++ b/README.md
@@ -1,2 +1,2 @@
 # playground
-old line
+new line
diff --git a/rename-me.txt b/renamed.txt
similarity index 67%
rename from rename-me.txt
rename to renamed.txt
diff --git a/data.bin b/data.bin
index 4444444..5555555 100644
GIT binary patch
literal 256
zcmV;B0R8^p+
`;

describe("parsePatch", () => {
  const files = parsePatch(patch);
  it("finds every file with its kind of change", () => {
    expect(files.map((f) => [f.path, f.status, f.binary])).toEqual([
      ["CONTRIBUTING.md", "added", false],
      ["README.md", "modified", false],
      ["rename-me.txt → renamed.txt", "renamed", false],
      ["data.bin", "modified", true],
    ]);
  });
  it("counts and classifies lines, skipping the ---/+++ headers", () => {
    expect(files[0]).toMatchObject({ added: 2, removed: 0 });
    expect(files[1].lines.map((l) => l.kind)).toEqual(["hunk", "ctx", "del", "add"]);
    expect(files[1]).toMatchObject({ added: 1, removed: 1 });
  });
  it("doesn't show binary data", () => {
    expect(files[3].lines).toEqual([]);
  });
});
