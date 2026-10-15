// parsePatch splits a git patch (git diff --binary) into files, each with
// its lines classified for display.

export type DiffLineKind = "hunk" | "add" | "del" | "ctx";

export interface DiffFile {
  path: string; // the new path ("old → new" for a rename)
  status: "added" | "deleted" | "modified" | "renamed";
  binary: boolean;
  added: number;
  removed: number;
  lines: { kind: DiffLineKind; text: string }[];
}

export function parsePatch(patch: string): DiffFile[] {
  const files: DiffFile[] = [];
  let cur: DiffFile | null = null;
  let inHunk = false;
  let renameFrom = "";

  for (const line of patch.split("\n")) {
    const head = /^diff --git a\/(.+) b\/(.+)$/.exec(line);
    if (head) {
      cur = { path: head[2], status: "modified", binary: false, added: 0, removed: 0, lines: [] };
      files.push(cur);
      inHunk = false;
      renameFrom = "";
      continue;
    }
    if (!cur) continue;
    if (!inHunk) {
      if (line.startsWith("new file mode")) cur.status = "added";
      else if (line.startsWith("deleted file mode")) cur.status = "deleted";
      else if (line.startsWith("rename from ")) renameFrom = line.slice("rename from ".length);
      else if (line.startsWith("rename to ")) {
        cur.status = "renamed";
        cur.path = `${renameFrom} → ${line.slice("rename to ".length)}`;
      } else if (line.startsWith("GIT binary patch") || line.startsWith("Binary files")) cur.binary = true;
    }
    if (cur.binary) continue;
    if (line.startsWith("@@")) {
      inHunk = true;
      cur.lines.push({ kind: "hunk", text: line });
    } else if (inHunk && line.startsWith("+")) {
      cur.added++;
      cur.lines.push({ kind: "add", text: line });
    } else if (inHunk && line.startsWith("-")) {
      cur.removed++;
      cur.lines.push({ kind: "del", text: line });
    } else if (inHunk && line.startsWith(" ")) {
      cur.lines.push({ kind: "ctx", text: line });
    }
  }
  return files;
}
