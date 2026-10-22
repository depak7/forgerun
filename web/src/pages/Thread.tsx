import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, ApiError } from "../api/client";
import { useAgents, useLiveLog, useLogTail, useModels, useNow, useDiff, useSession, useThread } from "../api/queries";
import type { Session, Task } from "../api/types";
import { StateLabel } from "../components/StateLabel";
import { agentChain, branchURL, formatElapsed, formatWhen, repoName, shortID } from "../format";
import { parsePatch } from "../lib/diff";
import { Summary } from "../lib/summary";
import { blockedByKey, keyState } from "../lib/keys";
import styles from "./Thread.module.css";

const isLive = (t: Task) => t.state === "queued" || t.state === "running";

// ThreadPage is one thread: every step as a conversation on the left, the
// selected step's log, diff and summary on the right, and the follow-up box.
export function ThreadPage() {
  const { id = "" } = useParams();
  const { data: steps, error, isLoading } = useThread(id);
  const [selected, setSelected] = useState<string | null>(null); // null = the newest step
  const [draft, setDraft] = useState("");
  const [planOnly, setPlanOnly] = useState(false); // the next message is plan-only (revising a plan)
  const queryClient = useQueryClient();
  const approve = useMutation({
    mutationFn: (latest: Task) =>
      api<Task>(`/api/tasks/${encodeURIComponent(latest.id)}/followups`, {
        method: "POST",
        body: JSON.stringify({ prompt: approvePrompt, agent: latest.agent, model: latest.model }),
      }),
    onSuccess: (_, latest) => {
      setSelected(null);
      queryClient.invalidateQueries({ queryKey: ["thread", latest.thread_id] });
      queryClient.invalidateQueries({ queryKey: ["threads"] });
    },
  });

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
                selected={s.id === sel.id}
                onSelect={() => setSelected(s.id)}
                onRetry={() => setDraft(s.prompt)}
                waiting={i === steps.length - 1 && s.state === "succeeded" && (s.plan || s.asked)}
              />
            ))}
          </div>
          {latest.state === "succeeded" && latest.plan && (
            <div className={styles.planBar} role="group" aria-label="Plan">
              <span>{latest.asked ? "The plan has open questions: answer them, or approve it as it is." : "The plan is ready."}</span>
              <button type="button" className={`${styles.btn} ${styles.btnPrimary}`} disabled={approve.isPending} onClick={() => approve.mutate(latest)}>
                {approve.isPending ? "[ starting… ]" : "[ approve & build ]"}
              </button>
              <button
                type="button"
                className={styles.btn}
                onClick={() => {
                  setPlanOnly(true);
                  document.getElementById("followup")?.focus();
                }}
              >
                [ revise ]
              </button>
            </div>
          )}
          {approve.error && <div className={styles.composerError}>Couldn't approve: {approve.error.message}</div>}
          <Composer
            latest={latest}
            n={steps.length}
            draft={draft}
            setDraft={setDraft}
            planOnly={planOnly}
            setPlanOnly={setPlanOnly}
            onSent={() => setSelected(null)}
          />
        </section>
        <OutputPane step={sel} n={selNumber} />
      </div>
    </div>
  );
}

function Header({ steps, latest }: { steps: Task[]; latest: Task }) {
  const queryClient = useQueryClient();
  const threadId = steps[0].id;
  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ["thread", threadId] });
    queryClient.invalidateQueries({ queryKey: ["session", threadId] });
  };
  const cancel = useMutation({
    mutationFn: () => api<Task>(`/api/tasks/${encodeURIComponent(latest.id)}/cancel`, { method: "POST" }),
    onSettled: refresh,
  });
  const push = useMutation({
    mutationFn: (pr: boolean) =>
      api<Task[]>(`/api/threads/${encodeURIComponent(threadId)}/push`, { method: "POST", body: JSON.stringify({ pr }) }),
    onSettled: refresh,
  });
  const end = useMutation({
    mutationFn: () => api<Session>(`/api/threads/${encodeURIComponent(threadId)}/end`, { method: "POST" }),
    onSettled: refresh,
  });
  const { data: session } = useSession(threadId);
  const pr = latest.pr;
  const unpushed = steps.filter((s) => s.state === "succeeded" && s.has_diff && !s.pushed).length;
  const idle = !isLive(latest) && !push.isPending;
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
            <span style={{ color: session?.open ? "var(--accent)" : undefined }}>{sessionLabel(session)}</span>
            <span style={{ color: pr.number ? "var(--accent)" : undefined }}>{pr.number ? `PR #${pr.number}` : pr.branch ? "branch pushed" : "not pushed"}</span>
            {unpushed > 0 && <span style={{ color: "var(--running)" }}>{unpushed} unpushed</span>}
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
          {session?.open && !session.busy && idle && (
            <button type="button" className={styles.btn} disabled={end.isPending} onClick={() => end.mutate()}>
              [ end session ]
            </button>
          )}
          {idle && unpushed > 0 && pr.number > 0 && (
            <button type="button" className={`${styles.btn} ${styles.btnPrimary}`} onClick={() => push.mutate(false)}>
              [ push to PR #{pr.number} ]
            </button>
          )}
          {idle && unpushed > 0 && !pr.number && (
            <button type="button" className={styles.btn} onClick={() => push.mutate(false)}>
              [ push branch ]
            </button>
          )}
          {idle && !pr.number && (unpushed > 0 || pr.branch) && (
            <button type="button" className={`${styles.btn} ${styles.btnPrimary}`} onClick={() => push.mutate(true)}>
              {unpushed > 0 ? "[ push & open PR ]" : "[ open PR ]"}
            </button>
          )}
          {push.isPending && <span className={styles.pending}>pushing…</span>}
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
      {push.error && <span style={{ color: "var(--failed)", fontSize: 13 }}>Couldn't push: {push.error.message}</span>}
      {end.error && <span style={{ color: "var(--failed)", fontSize: 13 }}>Couldn't end the session: {end.error.message}</span>}
    </header>
  );
}

// sessionLabel says whether the thread's VM is open, and until when.
function sessionLabel(session: Session | undefined): string {
  if (!session?.open) return "session closed";
  if (session.busy || !session.idle_until) return "session open";
  const until = new Date(session.idle_until).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  return `session open · closes ${until} if idle`;
}

// approvePrompt is the message that approves a plan.
const approvePrompt = "Approved. Go ahead and implement the plan.";

function StepItem(props: {
  step: Task;
  n: number;
  selected: boolean;
  onSelect: () => void;
  onRetry: () => void;
  waiting: boolean; // the newest step, and it waits on the user (a plan or questions)
}) {
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
          {pushLabel(s) && <span style={{ color: s.pushed || (s.plan && !s.asked) ? "var(--accent)" : "var(--running)" }}>{pushLabel(s)}</span>}
          <span>{timing(s, now)}</span>
        </span>
        <span className={styles.replyText}>
          {props.waiting && s.reply ? (s.plan ? "Here's the plan:" : "Before going on, the agent asks:") : replyText(s, tail)}
        </span>
        {s.state === "failed" && <span className={styles.replyError}>{s.error}</span>}
      </button>
      {props.waiting && s.reply && (
        <div className={styles.replyFull}>
          <Summary text={s.reply} headingClass={styles.summaryHeading} />
        </div>
      )}
      {s.state === "failed" && (
        <div className={styles.retry}>
          <button type="button" className={styles.btn} onClick={props.onRetry}>
            [ retry as follow-up ]
          </button>
          <span>this step's changes were discarded</span>
        </div>
      )}
    </div>
  );
}

// pushLabel says whether a step's changes are on the branch yet.
function pushLabel(s: Task): string {
  if (s.state === "succeeded" && s.asked) return "NEEDS YOUR ANSWER";
  if (s.state === "succeeded" && s.plan) return "PLAN";
  if (s.state !== "succeeded" || !s.has_diff) return "";
  return s.pushed ? "PUSHED" : "NOT PUSHED YET";
}

function timing(s: Task, now: Date): string {
  if (!s.started_at) return "waiting";
  const end = s.finished_at ? Date.parse(s.finished_at) : now.getTime();
  return formatElapsed(end - Date.parse(s.started_at));
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
  if (s.reply) return excerpt(s.reply);
  return s.has_diff ? "Finished; the changes are in the diff." : "Finished without changing any files.";
}

// excerpt is the start of an agent's reply as plain text, for the
// conversation; the reply tab shows all of it.
function excerpt(reply: string): string {
  const text = reply
    .split("\n")
    .map((l) => l.replace(/^\s*(#{1,6}|[-*]|\d+\.)\s+/, "").trim())
    .filter(Boolean)
    .join(" ")
    .replace(/`([^`]*)`/g, "$1") // drop code markers
    .replace(/\*\*([^*]*)\*\*/g, "$1"); // and bold
  return text.length > 320 ? `${text.slice(0, 317)}…` : text;
}

function Composer(props: {
  latest: Task;
  n: number;
  draft: string;
  setDraft: (s: string) => void;
  planOnly: boolean;
  setPlanOnly: (b: boolean) => void;
  onSent: () => void;
}) {
  const { latest, n } = props;
  const queryClient = useQueryClient();
  const { data: agents } = useAgents();
  const [agent, setAgent] = useState(latest.agent);
  const { data: models } = useModels(agent);
  const [model, setModel] = useState("");
  useEffect(() => {
    if (models) setModel(agent === latest.agent && models.models.some((m) => m.id === latest.model) ? latest.model : models.default);
  }, [models, agent, latest.agent, latest.model]);

  const send = useMutation({
    mutationFn: () =>
      api<Task>(`/api/tasks/${encodeURIComponent(latest.id)}/followups`, {
        method: "POST",
        body: JSON.stringify({ prompt: props.draft.trim(), agent, model, ...(props.planOnly ? { plan: true } : {}) }),
      }),
    onSuccess: () => {
      props.setDraft("");
      props.setPlanOnly(false);
      props.onSent();
      queryClient.invalidateQueries({ queryKey: ["thread", latest.thread_id] });
      queryClient.invalidateQueries({ queryKey: ["threads"] });
    },
  });
  const { data: session } = useSession(latest.thread_id);

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
  const missingKey = blockedByKey(agentInfo, models?.models.find((m) => m.id === model));
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
          placeholder={
            props.planOnly
              ? "What should change in the plan?"
              : latest.asked
                ? "Answer the agent's questions…"
                : "Reply to the agent, or ask for another change…"
          }
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
                  {keyState(a) === "missing" ? " (key missing)" : keyState(a) === "optional" ? " (free models only)" : ""}
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
          <label className={styles.composerCheck}>
            <input type="checkbox" checked={props.planOnly} onChange={(e) => props.setPlanOnly(e.target.checked)} /> plan only
          </label>
          <span className={styles.composerHint}>{composerHint(session, agent, latest.agent, missingKey ? agentInfo?.credential?.name : undefined)}</span>
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
    return `Step ${n} is ${latest.state} on ${latest.agent}. You can reply when it finishes, or cancel it from the header.`;
  }
  return "";
}

// composerHint says where the next message will run.
function composerHint(session: Session | undefined, agent: string, current: string, missingKey: string | undefined): string {
  if (missingKey) return `${agent} needs ${missingKey} on the server`;
  if (agent !== current) return `switches to ${agent}: a new session gets the conversation so far`;
  if (session?.open) return "continues the agent's conversation in its open session";
  return "reopens the session (a few seconds) with the conversation so far";
}

function OutputPane({ step, n }: { step: Task; n: number }) {
  const [tab, setTab] = useState<"log" | "diff" | "reply">("log");
  const start = step.snapshot_id !== null ? ` · warm from #${step.snapshot_id}` : "";
  return (
    <section className={styles.output} aria-label="Step output">
      <div className={styles.tabsRow}>
        <div role="tablist" aria-label="Output" className={styles.tabs}>
          {(["log", "diff", "reply"] as const).map((t) => (
            <button key={t} type="button" role="tab" aria-selected={tab === t} className={`${styles.tab} ${tab === t ? styles.tabOn : ""}`} onClick={() => setTab(t)}>
              {t}
            </button>
          ))}
        </div>
        <span className={styles.paneMeta}>
          step {n} · {step.agent}
          {step.started_at ? start : " · not started"}
        </span>
      </div>
      {tab === "log" && <LogView step={step} />}
      {tab === "diff" && <DiffView step={step} />}
      {tab === "reply" && <ReplyView step={step} />}
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

// ReplyView is everything the agent answered in a step and, once the
// thread is pushed with a pull request, the summary that describes it.
function ReplyView({ step }: { step: Task }) {
  if (!step.reply && !step.summary) {
    let why = "The agent didn't write anything for this step.";
    if (isLive(step)) why = "The agent's reply appears here when the step finishes.";
    return <div className={styles.empty}>{why}</div>;
  }
  return (
    <div className={styles.summary}>
      {step.reply && <Summary text={step.reply} headingClass={styles.summaryHeading} />}
      {step.summary && (
        <>
          <span className={styles.summaryHeading}>PULL REQUEST DESCRIPTION</span>
          <Summary text={step.summary} headingClass={styles.summaryHeading} />
        </>
      )}
    </div>
  );
}
