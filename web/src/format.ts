// formatBytes shows a size the way the server's logs do: "26.6 MB", "20 GB".
export function formatBytes(bytes: number): string {
  if (bytes >= 1e9) return `${trim(bytes / 1e9)} GB`;
  if (bytes >= 1e6) return `${trim(bytes / 1e6)} MB`;
  if (bytes >= 1e3) return `${trim(bytes / 1e3)} KB`;
  return `${bytes} B`;
}

function trim(n: number): string {
  return n.toFixed(1).replace(/\.0$/, "");
}
