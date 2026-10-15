import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, ApiError } from "../api/client";
import { useAgents, useLiveLog, useLogTail, useModels, useNow, useDiff, useThread } from "../api/queries";
import type { Task } from "../api/types";
import { StateLabel } from "../components/StateLabel";
import { agentChain, branchURL, formatElapsed, formatWhen, repoName, shortID } from "../format";
import { parsePatch } from "../lib/diff";
import { Summary } from "../lib/summary";
import styles from "./Thread.module.css";

const isLive = (t: Task) => t.state === "queued" || t.state === "running";

// ThreadPage is one thread: every step as a conversation on the left, the
// selected step's log, diff and summary on the right, and the follow-up box.
export function ThreadPage() {
  const { id = "" } = useParams();
  const { data: steps, error, isLoading } = useThread(id);
  const [selected, setSelected] = useState<string | null>(null); // null = the newest step
  const [draft, setDraft] = useState("");

  if (isLoading) return <p className="mono" style={{ color: "var(--muted)" }}>loading…</p>;
  if (error || !steps || steps.length === 0) {
    const notFound = error instanceof ApiError && error.status === 404;
    return (
      <p style={{ color: "var(--failed)" }}>
        {notFound ? "No such thread." : `Couldn't load the thread: ${error?.message ?? "empty"}`} <Link to="/">Back to tasks</Link>
      </p>
    );
  }

  const latest = steps[steps.length - 1];
  const sel = steps.find((s) => s.id === selected) ?? latest;
  const selNumber = steps.indexOf(sel) + 1;

  return (
    <div className={styles.page}>
      <Header steps={steps} latest={latest} />
      <div className={styles.split}>
        <section className={styles.conversation} aria-label="Conversation">
          <div className={styles.steps}>
            {steps.map((s, i) => (
              <StepItem
                key={s.id}
                step={s}
                n={i + 1}
                prevPR={i > 0 ? steps[i - 1].pr.number : 0}
                selected={s.id === sel.id}
                onSelect={() => setSelected(s.id)}
                onRetry={() => setDraft(s.prompt)}
              />
            ))}
          </div>
          <Composer latest={latest} n={steps.length} draft={draft} setDraft={setDraft} onSent={() => setSelected(null)} />
        </section>
        <OutputPane step={sel} n={selNumber} />
      </div>
    </div>
  );
}

function Header({ steps, latest }: { steps: Task[]; latest: Task }) {
  const queryClient = useQueryClient();
  const cancel = useMutation({
    mutationFn: () => api<Task>(`/api/tasks/${encodeURIComponent(latest.id)}/cancel`, { method: "POST" }),
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["thread", steps[0].id] }),
  });
  const pr = latest.pr;
  return (
    <header className={styles.header}>
      <div className={styles.crumbs}>
        <Link to="/">tasks</Link>
        <span aria-hidden="true">/</span>
        <span>{repoName(steps[0].repo)}</span>
        <span aria-hidden="true">/</span>
        <span>thread {shortID(steps[0].id)}</span>
      </div>
      <div className={styles.headRow}>
        <div style={{ display: "flex", flexDirection: "column", gap: 10, minWidth: 0, flex: "1 1 560px" }}>
          <h1 className={styles.title}>{steps[0].prompt}</h1>
          <div className={styles.facts}>
            <StateLabel state={latest.state} />
            <span style={{ color: pr.number ? "var(--accent)" : undefined }}>{pr.number ? `PR #${pr.number}` : pr.branch ? "branch only" : "not pushed"}</span>
            <span>
              {steps.length} {steps.length === 1 ? "step" : "steps"}
            </span>
            <span>{agentChain(steps.map((s) => s.agent))}</span>
          </div>
        </div>
        <div className={styles.buttons}>
          {isLive(latest) && (
            <button type="button" className={`${styles.btn} ${styles.btnDanger}`} disabled={cancel.isPending} onClick={() => cancel.mutate()}>
              [ cancel step {steps.length} ]
            </button>
          )}
          {pr.branch && (
            <a className={styles.btn} href={branchURL(latest.repo, pr.branch)} target="_blank" rel="noreferrer" title={pr.branch}>
              [ branch ↗ ]
            </a>
          )}
          {pr.url && (
            <a className={`${styles.btn} ${styles.btnPrimary}`} href={pr.url} target="_blank" rel="noreferrer">
              [ PR #{pr.number} ↗ ]
            </a>
          )}
        </div>
      </div>
      {cancel.error && <span style={{ color: "var(--failed)", fontSize: 13 }}>Couldn't cancel: {cancel.error.message}</span>}
    </header>
  );
}

function StepItem(props: { step: Task; n: number; prevPR: number; selected: boolean; onSelect: () => void; onRetry: () => void }) {
  const { step: s, n } = props;
  const now = useNow();
  const { data: tail } = useLogTail(s.id, s.state === "running");
  const replyClass = [
    styles.reply,
    s.state === "failed" ? styles.replyFailed : "",
    s.state === "running" ? styles.replyRunning : "",
    props.selected ? styles.replySelected : "",
  ].join(" ");
  return (
    <div className={styles.step}>
      <div className={styles.you}>
        <span className={styles.stamp}>
          you · step {n} · {formatWhen(s.created_at)}
        </span>
        <div className={styles.bubble}>{s.prompt}</div>
      </div>
      <button type="button" className={replyClass} aria-pressed={props.selected} aria-label={`Show step ${n} output`} onClick={props.onSelect}>
        <span className={styles.replyMeta}>
          <span>
            {s.agent} · {s.model}
            {s.effort && ` · ${s.effort}`}
          </span>
          <StateLabel state={s.state} />
          {pushAction(s, props.prevPR) && <span style={{ color: s.pr.error ? "var(--failed)" : "var(--accent)" }}>{pushAction(s, props.prevPR)}</span>}
          <span>{timing(s, now)}</span>
        </span>
        <span className={styles.replyText}>{replyText(s, tail)}</span>
        {(s.state === "failed" || (s.state === "succeeded" && s.pr.error)) && (
          <span className={styles.replyError}>{s.state === "failed" ? s.error : s.pr.error}</span>
        )}
      </button>
      {s.state === "failed" && (
        <div className={styles.retry}>
          <button type="button" className={styles.btn} onClick={props.onRetry}>
            [ retry as follow-up ]
          </button>
          <span>nothing was pushed for this step</span>
        </div>
      )}
    </div>
  );
}

// pushAction says where a step's changes went. A follow-up inherits its
// thread's branch and pull request, so prevPR tells opening from pushing.
function pushAction(s: Task, prevPR: number): string {
  if (s.state !== "succeeded") return "";
  if (s.pr.error) return s.pr.branch && s.has_diff ? "PR NOT OPENED" : "NOT PUSHED";
  if (!s.has_diff || !s.pr.branch) return "";
  if (!s.pr.number) return "PUSHED TO BRANCH";
  return s.pr.number !== prevPR ? `OPENED PR #${s.pr.number}` : `PUSHED TO PR #${s.pr.number}`;
}

function timing(s: Task, now: Date): string {
  const start = s.snapshot_id !== null ? "warm" : "cold";
  if (!s.started_at) return "waiting";
  const end = s.finished_at ? Date.parse(s.finished_at) : now.getTime();
  return `${start} · ${formatElapsed(end - Date.parse(s.started_at))}`;
}

// replyText is what the agent "says" for a step in the conversation.
function replyText(s: Task, tail: string | undefined): string {
  switch (s.state) {
    case "queued":
      return "Waiting for a free worker.";
    case "running":
      return `Working… ${tail ?? ""}`.trim();
    case "cancelled":
      return "Cancelled.";
    case "failed":
      return "Stopped before finishing.";
  }
  const done = sectionText(s.summary, "done");
  if (done) return done;
  return s.has_diff ? "Finished; the changes are in the diff." : "Finished without changing any files.";
}

// sectionText is the first lines of one "## Heading" section of a summary.
function sectionText(summary: string, heading: string): string {
  const m = new RegExp(`^#{1,6}\\s+${heading}\\s*$([\\s\\S]*?)(?=^#{1,6}\\s|$(?![\\s\\S]))`, "im").exec(summary);
  if (!m) return "";
  const text = m[1]
    .split("\n")
    .map((l) => l.replace(/^\s*[-*]\s+/, "").trim())
    .filter(Boolean)
    .join(" ")
    .replace(/`([^`]*)`/g, "$1"); // a plain excerpt: drop code markers
  return text.length > 280 ? `${text.slice(0, 277)}…` : text;
}

function Composer(props: { latest: Task; n: number; draft: string; setDraft: (s: string) => void; onSent: () => void }) {
  const { latest, n } = props;
  const queryClient = useQueryClient();
  const { data: agents } = useAgents();
  const [agent, setAgent] = useState(latest.agent);
  const { data: models } = useModels(agent);
  const [model, setModel] = useState("");
  const [openPR, setOpenPR] = useState(false);
  useEffect(() => {
    if (models) setModel(agent === latest.agent && models.models.some((m) => m.id === latest.model) ? latest.model : models.default);
  }, [models, agent, latest.agent, latest.model]);

  const send = useMutation({
    mutationFn: () =>
      api<Task>(`/api/tasks/${encodeURIComponent(latest.id)}/followups`, {
        method: "POST",
        body: JSON.stringify({ prompt: props.draft.trim(), agent, model, ...(openPR ? { pr: true } : {}) }),
      }),
    onSuccess: () => {
      props.setDraft("");
      props.onSent();
      queryClient.invalidateQueries({ queryKey: ["thread", latest.thread_id] });
      queryClient.invalidateQueries({ queryKey: ["threads"] });
    },
  });

  const closed = closedReason(latest, n);
  if (closed) {
    return (
      <div className={styles.composerWrap}>
        <div className={styles.closed} role="status">
          <span style={{ flex: "1 1 300px" }}>{closed}</span>
          {!isLive(latest) && (
            <Link className={styles.btn} to={`/tasks/new?repo=${encodeURIComponent(latest.repo)}`}>
              [ new task on this repo ]
            </Link>
          )}
        </div>
      </div>
    );
  }
  const agentInfo = agents?.find((a) => a.id === agent);
  const missingKey = agentInfo?.credential && !agentInfo.credential.set;
  const canSend = props.draft.trim() !== "" && model !== "" && !missingKey && !send.isPending;
  return (
    <div className={styles.composerWrap}>
      <form
        className={styles.composer}
        onSubmit={(e) => {
          e.preventDefault();
          if (canSend) send.mutate();
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey) && canSend) {
            e.preventDefault();
            send.mutate();
          }
        }}
      >
        <label htmlFor="followup" className="sr-only">
          Follow up on this thread
        </label>
        <textarea
          id="followup"
          rows={3}
          value={props.draft}
          onChange={(e) => props.setDraft(e.target.value)}
          placeholder={latest.pr.number ? `Ask for another change to PR #${latest.pr.number}…` : `Ask for another change on ${latest.pr.branch}…`}
        />
        <div className={styles.composerBar}>
          <label htmlFor="fu-agent" className="sr-only">
            Agent
          </label>
          <select id="fu-agent" value={agent} onChange={(e) => setAgent(e.target.value)}>
            {agents
              ?.filter((a) => a.runnable)
              .map((a) => (
                <option key={a.id} value={a.id}>
                  {a.id}
                  {a.credential && !a.credential.set ? " (key missing)" : ""}
                </option>
              ))}
          </select>
          <label htmlFor="fu-model" className="sr-only">
            Model
          </label>
          <select id="fu-model" value={model} onChange={(e) => setModel(e.target.value)}>
            {models?.models.map((m) => (
              <option key={m.id} value={m.id}>
                {m.id}
              </option>
            ))}
          </select>
          {!latest.pr.number && (
            <label className={styles.composerCheck}>
              <input type="checkbox" checked={openPR} onChange={(e) => setOpenPR(e.target.checked)} /> open a pull request
            </label>
          )}
          <span className={styles.composerHint}>
            {missingKey
              ? `${agent} needs ${agentInfo?.credential?.name} on the server`
              : `continues ${latest.pr.number ? `PR #${latest.pr.number}'s branch` : "the branch"} with the thread summary`}
          </span>
          <button type="submit" className={`${styles.btn} ${styles.btnPrimary} ${styles.send}`} disabled={!canSend}>
            {send.isPending ? "[ sending… ]" : "[ send ]"}
          </button>
        </div>
      </form>
      {send.error && <div className={styles.composerError}>The server refused: {send.error.message}</div>}
    </div>
  );
}

// closedReason explains why the thread can't take a follow-up now, or "".
function closedReason(latest: Task, n: number): string {
  if (isLive(latest)) {
    return `Step ${n} is ${latest.state} on ${latest.agent}. You can follow up when it finishes, or cancel it from the header.`;
  }
  if (!latest.pr.branch) {
    if (latest.pr.error) {
      return `The changes weren't pushed (${latest.pr.error}), so there's no branch to continue. Start a new task to try again.`;
    }
    return "The agent made no changes, so there's no branch to continue. Start a new task instead.";
  }
  return "";
}

function OutputPane({ step, n }: { step: Task; n: number }) {
  const [tab, setTab] = useState<"log" | "diff" | "summary">("log");
  const start = step.snapshot_id !== null ? `warm from #${step.snapshot_id}` : "cold";
  return (
    <section className={styles.output} aria-label="Step output">
      <div className={styles.tabsRow}>
        <div role="tablist" aria-label="Output" className={styles.tabs}>
          {(["log", "diff", "summary"] as const).map((t) => (
            <button key={t} type="button" role="tab" aria-selected={tab === t} className={`${styles.tab} ${tab === t ? styles.tabOn : ""}`} onClick={() => setTab(t)}>
              {t}
            </button>
          ))}
        </div>
        <span className={styles.paneMeta}>
          step {n} · {step.agent} · {step.started_at ? start : "not started"}
        </span>
      </div>
      {tab === "log" && <LogView step={step} />}
      {tab === "diff" && <DiffView step={step} />}
      {tab === "summary" && <SummaryView step={step} />}
    </section>
  );
}

function LogView({ step }: { step: Task }) {
  const live = isLive(step);
  const { text, error } = useLiveLog(step.id, live);
  const box = useRef<HTMLPreElement>(null);
  const [following, setFollowing] = useState(true);
  useEffect(() => {
    if (following && box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [text, following]);

  if (!text && step.state === "queued") return <div className={styles.empty}>Waiting for a free worker. The log starts when the VM does.</div>;
  if (!text && !live && !error) return <div className={styles.empty}>No log for this step: it stopped before the VM started.</div>;
  return (
    <>
      <pre
        ref={box}
        className={styles.log}
        onScroll={(e) => {
          const el = e.currentTarget;
          setFollowing(el.scrollHeight - el.scrollTop - el.clientHeight < 40);
        }}
      >
        {text.split("\n").map((line, i) => (
          <div key={i} className={logClass(line)}>
            {line || " "}
          </div>
        ))}
        {error && <div className={styles.logBad}>{error}</div>}
      </pre>
      {live && (
        <div className={styles.follow}>
          <span>
            <span className="live" style={{ color: "var(--running)" }}>
              ●
            </span>{" "}
            {following ? "FOLLOWING LIVE OUTPUT" : "PAUSED: YOU SCROLLED UP"}
          </span>
          {!following && (
            <button type="button" className={styles.btn} style={{ minHeight: 32 }} onClick={() => setFollowing(true)}>
              jump to latest ↓
            </button>
          )}
        </div>
      )}
    </>
  );
}

function logClass(line: string): string {
  if (/^==> (warning|stopped the agent)/.test(line) || /^Connection lost/.test(line)) return styles.logBad;
  if (line.startsWith("==>")) return styles.logStep;
  return "";
}

function DiffView({ step }: { step: Task }) {
  const done = !isLive(step);
  const { data: diff, isLoading, error } = useDiff(step.id, done && step.has_diff);
  if (!done) return <div className={styles.empty}>Changes appear here when the run finishes and forgerun collects them.</div>;
  if (!step.has_diff) {
    return (
      <div className={styles.empty}>
        {step.state === "succeeded" ? "The agent made no changes." : "No changes were collected: the step didn't finish."}
      </div>
    );
  }
  if (isLoading) return <div className={styles.empty}>loading…</div>;
  if (error) return <div className={styles.empty}>Couldn't load the diff: {error.message}</div>;
  return (
    <div className={styles.paneBody}>
      {parsePatch(diff ?? "").map((f) => (
        <div key={f.path} className={styles.file}>
          <div className={styles.fileHead}>
            <span>
              {f.path} {f.status !== "modified" && <span style={{ color: "var(--faint)" }}>({f.status})</span>}
            </span>
            <span>
              {f.binary ? (
                <span style={{ color: "var(--faint)" }}>binary changed</span>
              ) : (
                <>
                  <span style={{ color: "var(--accent)" }}>+{f.added}</span> <span style={{ color: "var(--failed)" }}>−{f.removed}</span>
                </>
              )}
            </span>
          </div>
          <div className={styles.diff}>
            {f.lines.map((l, i) => (
              <div key={i} className={`${styles.diffLine} ${l.kind === "add" ? styles.diffAdd : l.kind === "del" ? styles.diffDel : l.kind === "hunk" ? styles.diffHunk : ""}`}>
                {l.text}
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

function SummaryView({ step }: { step: Task }) {
  if (step.summary) {
    return (
      <div className={styles.summary}>
        <Summary text={step.summary} headingClass={styles.summaryHeading} />
        {step.pr.url && <span className={styles.summaryNote}>The latest summary is PR #{step.pr.number}'s description. Each follow-up rewrites it for the whole thread.</span>}
      </div>
    );
  }
  let why = "No summary for this step.";
  if (isLive(step)) why = "The agent writes a summary of the whole thread when the run finishes.";
  else if (step.state !== "succeeded") why = "No summary: the step didn't finish.";
  else if (!step.has_diff) why = "No summary: the agent made no changes.";
  return <div className={styles.empty}>{why}</div>;
}
