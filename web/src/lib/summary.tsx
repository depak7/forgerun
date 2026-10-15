import type { ReactNode } from "react";

// Summary renders an agent's Markdown summary (## headings, - bullets,
// paragraphs, `code`) as plain React elements: no raw HTML, so nothing
// in the agent's text can inject markup.
export function Summary({ text, headingClass }: { text: string; headingClass?: string }) {
  const blocks: ReactNode[] = [];
  let bullets: string[] = [];
  const flush = () => {
    if (bullets.length > 0) {
      blocks.push(
        <ul key={blocks.length} style={{ margin: 0, paddingLeft: 20, display: "flex", flexDirection: "column", gap: 4 }}>
          {bullets.map((b, i) => (
            <li key={i}>{inline(b)}</li>
          ))}
        </ul>,
      );
      bullets = [];
    }
  };
  for (const raw of text.split("\n")) {
    const line = raw.trimEnd();
    const heading = /^#{1,6}\s+(.*)$/.exec(line);
    const bullet = /^\s*[-*]\s+(.*)$/.exec(line);
    if (bullet) {
      bullets.push(bullet[1]);
      continue;
    }
    flush();
    if (heading) {
      blocks.push(
        <span key={blocks.length} className={headingClass}>
          ## {heading[1].toUpperCase()}
        </span>,
      );
    } else if (line.trim() !== "") {
      blocks.push(<p key={blocks.length} style={{ margin: 0 }}>{inline(line)}</p>);
    }
  }
  flush();
  return <>{blocks}</>;
}

// inline turns `code` into monospace; everything else stays text.
function inline(s: string): ReactNode[] {
  return s.split(/(`[^`]+`|\*\*[^*]+\*\*)/).map((part, i) => {
    if (part.startsWith("`") && part.endsWith("`") && part.length > 2) {
      return (
        <code key={i} className="mono" style={{ fontSize: "0.92em", color: "var(--text)" }}>
          {part.slice(1, -1)}
        </code>
      );
    }
    if (part.startsWith("**") && part.endsWith("**") && part.length > 4) {
      return (
        <strong key={i} style={{ fontWeight: 600, color: "var(--text)" }}>
          {part.slice(2, -2)}
        </strong>
      );
    }
    return part;
  });
}
