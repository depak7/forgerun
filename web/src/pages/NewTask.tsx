import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { api } from "../api/client";
import { useAgents, useModels, useSnapshots, useStatus, useThreads } from "../api/queries";
import type { Agent, ModelFamily, NewTask, Task } from "../api/types";
import { checkRepo, isGitHub, normalizeRepo } from "../format";
import styles from "./NewTask.module.css";

// NewTaskPage starts a thread: what an agent should do, on which repo, with
// which agent and model. Nothing is pushed until the user pushes the thread.
export function NewTaskPage() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { data: agents } = useAgents();
  const { data: status } = useStatus();
  const { data: snapshots } = useSnapshots();
  const { data: threads } = useThreads();

  const [repo, setRepo] = useState(params.get("repo") ?? "");
  const [prompt, setPrompt] = useState(params.get("prompt") ?? "");
  const [agentId, setAgentId] = useState("");
  const [family, setFamily] = useState("");
  const [effort, setEffort] = useState(""); // "" = the family's default
  const [thinking, setThinking] = useState(false);
  const [fast, setFast] = useState(false);
  const [planFirst, setPlanFirst] = useState(false);

  // Defaults once data arrives: the most recent repo, and the first agent
  // with its credential set.
  useEffect(() => {
    if (repo === "" && threads && threads.length > 0) setRepo(threads[0].repo);
    // Only when threads first arrive: never overwrite what the user typed.
  }, [threads]);
  useEffect(() => {
    if (agentId === "" && agents && agents.length > 0) {
      const runnable = agents.filter((a) => a.runnable);
      setAgentId((runnable.find((a) => a.credential?.set) ?? runnable[0] ?? agents[0]).id);
    }
  }, [agents, agentId]);

  const { data: modelList } = useModels(agentId);
  useEffect(() => {
    if (modelList) {
      setFamily(modelList.default);
      setEffort("");
      setThinking(false);
      setFast(false);
    }
  }, [modelList]);
  const fam: ModelFamily | undefined = modelList?.models.find((m) => m.id === family);

  const agent = agents?.find((a) => a.id === agentId);
  const repoProblem = checkRepo(repo);
  const warm = useMemo(
    () => !!snapshots?.some((s) => s.state === "current" && s.agent === agentId && s.repo === normalizeRepo(repo)),
    [snapshots, agentId, repo],
  );

  const body: NewTask = {
    repo: repo.trim(),
    prompt: prompt.trim(),
    agent: agentId,
    model: family,
    ...(effort ? { effort } : {}),
    ...(thinking ? { thinking: true } : {}),
    ...(fast ? { fast: true } : {}),
    ...(planFirst ? { plan: true } : {}),
  };

  const create = useMutation({
    mutationFn: (b: NewTask) => api<Task>("/api/tasks", { method: "POST", body: JSON.stringify(b) }),
    onSuccess: (task) => {
      queryClient.invalidateQueries({ queryKey: ["threads"] });
      navigate(`/threads/${task.thread_id}`);
    },
  });

  const blocked = blockedReason(repoProblem, repo, agent, prompt, family);
  const submit = () => {
    if (!blocked && !create.isPending) create.mutate(body);
  };

  return (
    <div className={styles.page}>
      <div className={styles.crumbs}>
        <Link to="/">tasks</Link>
        <span aria-hidden="true">/</span>
        <span style={{ color: "var(--text)" }}>new</span>
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
        <span className="label">02 | New task</span>
        <h1 className={styles.title}>What should the agent do?</h1>
      </div>

      <div className={styles.columns}>
        <form
          className={styles.form}
          onSubmit={(e) => {
            e.preventDefault();
            submit();
          }}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              submit();
            }
          }}
        >
          <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
            <div className={`${styles.composer} ${repo && repoProblem ? styles.composerBad : ""}`}>
              <div className={styles.repoRow}>
                <label htmlFor="repo">repo</label>
                <input
                  id="repo"
                  className={styles.repoInput}
                  type="url"
                  value={repo}
                  onChange={(e) => setRepo(e.target.value)}
                  placeholder="https://github.com/owner/name"
                  aria-describedby="repo-hint"
                  aria-invalid={repo !== "" && repoProblem !== ""}
                />
                <span className={`${styles.badge} ${repoProblem ? styles.badgeBad : warm ? styles.badgeWarm : ""}`}>
                  {repoProblem ? "INVALID" : warm ? "● WARM SNAPSHOT" : "COLD START"}
                </span>
              </div>
              <label htmlFor="prompt" className="sr-only">
                Task for the agent
              </label>
              <textarea
                id="prompt"
                className={styles.prompt}
                rows={6}
                value={prompt}
                onChange={(e) => setPrompt(e.target.value)}
                placeholder="e.g. Fix the failing date parsing test and add a regression test"
              />
            </div>
            <span id="repo-hint" className={`${styles.hint} ${repo && repoProblem ? styles.hintBad : ""}`}>
              {repo && repoProblem
                ? repoProblem
                : warm
                  ? "A snapshot of this repo exists for this agent: it starts in seconds."
                  : "The first task on a repo starts cold (clone); later ones start warm."}
            </span>
          </div>

          <fieldset className={styles.fieldset}>
            <legend className={`label ${styles.legend}`}>Agent</legend>
            <div className={styles.agents}>
              {agents?.map((a) => {
                const on = a.id === agentId;
                const set = a.credential?.set ?? false;
                return (
                  <button
                    key={a.id}
                    type="button"
                    aria-pressed={on}
                    disabled={!a.runnable}
                    onClick={() => setAgentId(a.id)}
                    className={`${styles.agent} ${on ? (set ? styles.agentOn : styles.agentOnMissing) : ""}`}
                  >
                    <span className={styles.agentTop}>
                      <span>{a.id}</span>
                      <span className={`${styles.key} ${set ? styles.keySet : styles.keyMissing}`}>{set ? "KEY SET" : "KEY MISSING"}</span>
                    </span>
                    <span className={styles.agentNote}>{agentNote(a)}</span>
                  </button>
                );
              })}
            </div>
          </fieldset>

          <div className={styles.modelRow}>
            <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
              <label htmlFor="model" className="label">
                Model
              </label>
              <select
                id="model"
                className={styles.select}
                value={family}
                onChange={(e) => {
                  setFamily(e.target.value);
                  setEffort("");
                  setThinking(false);
                  setFast(false);
                }}
              >
                {modelList?.models.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.name}
                    {m.zdr ? "" : " (no ZDR)"}
                  </option>
                ))}
              </select>
              {(fam?.thinking || fam?.fast) && (
                <div className={styles.checks}>
                  {fam.thinking && (
                    <label>
                      <input type="checkbox" checked={thinking} onChange={(e) => setThinking(e.target.checked)} /> thinking
                    </label>
                  )}
                  {fam.fast && (
                    <label>
                      <input type="checkbox" checked={fast} onChange={(e) => setFast(e.target.checked)} /> fast
                    </label>
                  )}
                </div>
              )}
            </div>
            <fieldset className={styles.fieldset}>
              <legend className={`label ${styles.legend}`}>Effort</legend>
              {fam && fam.efforts.length > 0 ? (
                <div className={styles.pills}>
                  {["", ...fam.efforts.filter((e) => e !== fam.default_effort || fam.default_effort === "")].map((e) => (
                    <button
                      key={e || "default"}
                      type="button"
                      aria-pressed={effort === e}
                      onClick={() => setEffort(e)}
                      className={`${styles.pill} ${effort === e ? styles.pillOn : ""}`}
                    >
                      {e === "" ? (fam.default_effort ? `default (${fam.default_effort})` : "default") : e}
                    </button>
                  ))}
                </div>
              ) : (
                <span className="mono" style={{ minHeight: 44, display: "flex", alignItems: "center", fontSize: 13, color: "var(--muted)" }}>
                  model decides
                </span>
              )}
            </fieldset>
          </div>

          <div className={styles.prBox}>
            <button
              type="button"
              role="switch"
              aria-checked={planFirst}
              aria-label="Plan first"
              className={styles.switch}
              onClick={() => setPlanFirst(!planFirst)}
            >
              <span className={styles.knob} />
            </button>
            <div className={styles.prText}>
              <span style={{ fontWeight: 500 }}>Plan first</span>
              <span className={styles.prNote}>
                The agent reads the repo and proposes a plan without changing anything; you approve it or ask for changes.
              </span>
            </div>
          </div>
          <p className={styles.prNote}>{pushNote(status?.pull_requests, repoProblem, repo)}</p>

          <div className={styles.actions}>
            {(blocked || create.error) && (
              <span role="status" className={styles.blocked}>
                {create.error ? `The server refused: ${create.error.message}` : blocked}
              </span>
            )}
            <span className={styles.shortcut}>⌘ + Enter</span>
            <Link to="/" className={styles.secondary}>
              [ cancel ]
            </Link>
            <button type="submit" className={styles.run} disabled={!!blocked || create.isPending}>
              {create.isPending ? "[ starting… ]" : "[ run task ]"}
            </button>
          </div>
        </form>

        <details className={styles.preview}>
          <summary>POST /api/tasks</summary>
          <pre>{JSON.stringify(body, null, 2)}</pre>
        </details>
      </div>
    </div>
  );
}

// blockedReason explains why the task can't be started yet, or "".
function blockedReason(repoProblem: string, repo: string, agent: Agent | undefined, prompt: string, family: string): string {
  if (repoProblem) return repoProblem;
  if (!isGitHub(repo)) return "forgerun pushes each task's changes to a branch, so the repo must be on github.com.";
  if (!agent) return "Pick an agent.";
  if (!agent.runnable) return `forgerun has no adapter for ${agent.id}.`;
  if (agent.credential && !agent.credential.set) {
    return `${agent.id} has no credential on this server: set ${agent.credential.name}, or pick another agent.`;
  }
  if (!family) return "Pick a model.";
  if (prompt.trim() === "") return "Say what the agent should do.";
  return "";
}

function agentNote(a: Agent): string {
  if (!a.runnable) return "Not available on this server.";
  if (a.credential && !a.credential.set) return `Needs ${a.credential.name} on the server.`;
  return `${a.name}; default model ${a.default_model}.`;
}

function pushNote(prs: { enabled: boolean; github_user: string } | undefined, repoProblem: string, repo: string): string {
  if (!prs?.enabled) return "This server can't push (no GITHUB_TOKEN).";
  if (repoProblem || !isGitHub(repo)) return "Needs a github.com repo, so the changes can be pushed when you're done.";
  return `The agent works in a session you can keep talking to. Nothing is pushed until you press push: then the changes go to a forgerun/… branch as ${prs.github_user}, and a pull request if you want one.`;
}
