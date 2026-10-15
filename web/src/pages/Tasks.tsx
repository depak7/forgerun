import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { useAgents, useLogTail, useNow, useStatus, useThreads } from "../api/queries";
import type { Thread } from "../api/types";
import { StateLabel } from "../components/StateLabel";
import { agentChain, formatElapsed, formatWhen, repoName, shortID } from "../format";
import styles from "./Tasks.module.css";

type Filter = "all" | "attention" | "succeeded" | "failed";

// TasksPage is the home screen: what's running now, then every thread.
export function TasksPage() {
  const { data: threads, isLoading, error } = useThreads();
  const [filter, setFilter] = useState<Filter>("all");
  const [query, setQuery] = useState("");

  const live = (threads ?? []).filter((t) => t.latest.state === "running" || t.latest.state === "queued");
  const attention = (threads ?? []).filter((t) => t.needs_attention).length;
  const shown = useMemo(() => filterThreads(threads ?? [], filter, query), [threads, filter, query]);

  return (
    <>
      <div className={styles.head}>
        <div className={styles.headText}>
          <span className="label">01 | Tasks</span>
          <h1 className={styles.title}>Agents at work</h1>
          <p className={styles.lede}>Each task runs in its own VM and pushes its changes to a branch, with a pull request if you ask.</p>
        </div>
        <Link to="/tasks/new" className={styles.primary}>
          [ new task ]
        </Link>
      </div>

      {error && <p className={styles.problem}>Couldn't load tasks: {error.message}</p>}
      {isLoading && <p className="mono" style={{ color: "var(--muted)" }}>loading…</p>}

      {threads && threads.length === 0 && <FirstRun />}

      {threads && threads.length > 0 && (
        <>
          {live.length > 0 && (
            <section className={styles.section} aria-label="Running and queued">
              <span className="label">Now</span>
              {live.map((t) => (
                <NowRow key={t.latest.id} thread={t} />
              ))}
            </section>
          )}

          <section className={styles.section} aria-label="Threads">
            <div className={styles.toolbar}>
              <span className={`label ${styles.toolbarLabel}`}>Threads</span>
              <FilterButton on={filter === "all"} onClick={() => setFilter("all")}>
                ALL
              </FilterButton>
              <FilterButton on={filter === "attention"} attention onClick={() => setFilter("attention")}>
                NEEDS ATTENTION · {attention}
              </FilterButton>
              <FilterButton on={filter === "succeeded"} onClick={() => setFilter("succeeded")}>
                SUCCEEDED
              </FilterButton>
              <FilterButton on={filter === "failed"} onClick={() => setFilter("failed")}>
                FAILED
              </FilterButton>
              <label htmlFor="search" className="sr-only">
                Search threads
              </label>
              <input
                id="search"
                type="search"
                className={styles.search}
                placeholder="$ grep prompts, repos"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
            </div>
            <div className={styles.list}>
              {shown.map((t) => (
                <ThreadRow key={t.id} thread={t} />
              ))}
              {shown.length === 0 && (
                <div className={styles.nothing}>
                  {filter === "attention" ? "Nothing needs attention. Every thread is fine." : "No threads match."}
                </div>
              )}
            </div>
          </section>
        </>
      )}
    </>
  );
}

function filterThreads(threads: Thread[], filter: Filter, query: string): Thread[] {
  const q = query.trim().toLowerCase();
  return threads.filter((t) => {
    if (filter === "attention" && !t.needs_attention) return false;
    if ((filter === "succeeded" || filter === "failed") && t.latest.state !== filter) return false;
    if (!q) return true;
    return [t.title, t.repo, t.latest.prompt].some((s) => s.toLowerCase().includes(q));
  });
}

function FilterButton(props: { on: boolean; attention?: boolean; onClick: () => void; children: React.ReactNode }) {
  const cls = [styles.filter, props.attention && !props.on ? styles.filterAttention : "", props.on ? styles.filterOn : ""].join(" ");
  return (
    <button type="button" className={cls} aria-pressed={props.on} onClick={props.onClick}>
      {props.children}
    </button>
  );
}

// NowRow is a running or queued step, with the last line its agent wrote.
function NowRow({ thread }: { thread: Thread }) {
  const t = thread.latest;
  const running = t.state === "running";
  const now = useNow();
  const { data: tail } = useLogTail(t.id, running);
  const repo = repoName(t.repo);
  const context = thread.steps > 1 ? `step ${thread.steps} of ${prLabel(thread) ?? "this thread"} · ${repo}` : `new thread · ${repo}`;
  return (
    <Link to={`/threads/${thread.id}`} className={`${styles.row} ${styles.nowRow} ${running ? styles.nowRowLive : ""}`}>
      <span className={styles.stateCol}>
        <StateLabel state={t.state} />
        <span className={styles.id}>{shortID(t.id)}</span>
      </span>
      <span className={styles.what}>
        <span className={styles.prompt}>{t.prompt}</span>
        <span className={styles.meta}>{context}</span>
        <span className={styles.tail}>{running ? tail || "starting…" : "waiting for a free worker"}</span>
      </span>
      <span className={styles.agentCol}>
        <span>{t.agent}</span>
        <span className={styles.soft}>{modelLabel(t.model, t.effort)}</span>
      </span>
      <span className={styles.when}>{running && t.started_at ? formatElapsed(now.getTime() - Date.parse(t.started_at)) : "next"}</span>
    </Link>
  );
}

// ThreadRow is one thread: its first request, and how its newest step went.
function ThreadRow({ thread }: { thread: Thread }) {
  const t = thread.latest;
  const pr = prLabel(thread);
  const pushFailed = t.state === "succeeded" && t.pr.error !== "";
  const note = t.state === "failed" ? t.error : pushFailed ? t.pr.error : "";
  return (
    <Link to={`/threads/${thread.id}`} className={styles.row}>
      <span className={styles.stateCol}>
        <StateLabel state={t.state} />
        {pushFailed && <span className={styles.problem}>PUSH FAILED</span>}
        {thread.session_open && <span className={styles.session}>● SESSION OPEN</span>}
      </span>
      <span className={styles.what}>
        <span className={styles.prompt}>{thread.title}</span>
        <span className={styles.meta}>
          <span>{repoName(thread.repo)}</span>
          {pr && <span className={styles.prLink}>{pr}</span>}
          <span>
            {thread.steps} {thread.steps === 1 ? "step" : "steps"}
          </span>
        </span>
        {thread.steps > 1 && (
          <span className={styles.latest}>
            ↳ step {thread.steps} {t.state}: {t.prompt}
          </span>
        )}
        {note && <span className={`${styles.meta} ${styles.problem}`}>{note}</span>}
      </span>
      <span className={styles.agentCol}>
        <span>{agentChain(thread.agents)}</span>
        <span className={styles.soft}>{modelLabel(t.model, t.effort)}</span>
      </span>
      <span className={styles.when}>{formatWhen(thread.updated_at)}</span>
    </Link>
  );
}

function prLabel(thread: Thread): string | null {
  const pr = thread.latest.pr;
  if (pr.number) return `PR #${pr.number}`;
  if (pr.branch) return "branch pushed";
  return thread.latest.has_diff && !thread.latest.pushed ? "not pushed yet" : null;
}

function modelLabel(model: string, effort: string): string {
  return effort ? `${model} · ${effort}` : model;
}

// FirstRun replaces the lists until there's a first task.
function FirstRun() {
  const { data: agents } = useAgents();
  const { data: status } = useStatus();
  const ready = agents?.filter((a) => a.credential?.set).map((a) => a.id) ?? [];
  const missing = agents?.filter((a) => a.credential && !a.credential.set) ?? [];
  return (
    <section className={styles.firstRun} aria-label="Get started">
      <div style={{ display: "flex", flexDirection: "column", gap: 6 }}>
        <span className="label">No tasks yet</span>
        <h2>Give an agent its first task</h2>
        <p>Point it at a repo and say what to change. It works in its own VM; you watch the log live and get a branch, or a pull request.</p>
      </div>
      <ol className={styles.steps}>
        <li>
          <span className={styles.stepNum}>1</span>
          <span>
            <strong style={{ fontWeight: 500 }}>Credentials:</strong> {ready.length > 0 ? `${ready.join(", ")} ready.` : "no agent has a credential yet."}{" "}
            {missing.map((a) => (
              <span key={a.id}>
                {a.id} needs <span className="mono">{a.credential?.name}</span> on the server.{" "}
              </span>
            ))}
          </span>
        </li>
        <li>
          <span className={styles.stepNum}>2</span>
          <span>
            <strong style={{ fontWeight: 500 }}>Pull requests:</strong>{" "}
            {status?.pull_requests.enabled
              ? `GitHub is connected as ${status.pull_requests.github_user}, so tasks can open PRs.`
              : "off. Set GITHUB_TOKEN on the server to open PRs; tasks still produce patches."}
          </span>
        </li>
        <li>
          <span className={styles.stepNum}>3</span>
          <span>
            <strong style={{ fontWeight: 500 }}>Try one:</strong>
          </span>
        </li>
      </ol>
      <div className={styles.examples}>
        {["Add a CONTRIBUTING.md with three short tips for contributors", "Find and fix one flaky test"].map((p) => (
          <Link key={p} to={`/tasks/new?prompt=${encodeURIComponent(p)}`} className={styles.example}>
            {p}
          </Link>
        ))}
      </div>
    </section>
  );
}
