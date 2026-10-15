import { useAgents, useModels, useSnapshots, useStatus } from "../api/queries";
import type { Agent, Snapshot, Status } from "../api/types";
import { formatBytes, formatDuration, formatWhen, repoName } from "../format";
import styles from "./Settings.module.css";

// SettingsPage shows how this server is set up: which agents can run and
// whether their keys are set, the models each offers, GitHub (branches and
// pull requests) and snapshots. It changes nothing: all of it comes from .env and the flags
// `forgerun serve` was started with.
export function SettingsPage() {
  const { data: agents } = useAgents();
  const { data: status } = useStatus();

  return (
    <div className={styles.page}>
      <div className={styles.headText}>
        <span className="label">03 | Settings</span>
        <h1 className={styles.title}>How this server is set up</h1>
        <p className={styles.lede}>
          Read-only. Keys come from <code>.env</code>, limits from the flags of <code>forgerun serve</code>: change them
          there and restart the server.
        </p>
      </div>

      <section className={styles.section} aria-labelledby="keys">
        <h2 id="keys" className="label">
          Agents &amp; keys
        </h2>
        <div className={styles.list}>
          {agents?.map((a) => <AgentRow key={a.id} agent={a} />)}
        </div>
      </section>

      <section className={styles.section} aria-labelledby="prs">
        <h2 id="prs" className="label">
          GitHub
        </h2>
        <div className={styles.card}>{status && <PullRequests status={status} />}</div>
      </section>

      <section className={styles.section} aria-labelledby="sessions">
        <h2 id="sessions" className="label">
          Sessions
        </h2>
        <div className={styles.card}>{status && <Sessions status={status} />}</div>
      </section>

      <section className={styles.section} aria-labelledby="snapshots">
        <h2 id="snapshots" className="label">
          Snapshots
        </h2>
        {status && <Snapshots status={status} />}
      </section>

      <section className={styles.section} aria-labelledby="models">
        <h2 id="models" className="label">
          Models
        </h2>
        {status?.require_zdr && (
          <p className={styles.note}>
            This server accepts only models with zero data retention (ZDR); tasks asking for others are refused.
          </p>
        )}
        {agents?.filter((a) => a.runnable).map((a) => <AgentModels key={a.id} agent={a} requireZDR={!!status?.require_zdr} />)}
      </section>
    </div>
  );
}

function AgentRow({ agent: a }: { agent: Agent }) {
  const set = a.credential?.set ?? true; // no credential needed = nothing missing
  return (
    <div className={styles.row}>
      <div className={styles.agentName}>
        <span className="mono">{a.id}</span>
        <span className={styles.sub}>{a.name}</span>
      </div>
      <div className={styles.agentKey}>
        {a.credential ? (
          <>
            <code>{a.credential.name}</code>
            <span className={`${styles.tag} ${set ? styles.good : styles.bad}`}>{set ? "KEY SET" : "KEY MISSING"}</span>
          </>
        ) : (
          <span className={styles.sub}>needs no key</span>
        )}
      </div>
      <div className={styles.agentState}>
        {!a.runnable ? (
          <span className={styles.sub}>no adapter on this server</span>
        ) : set ? (
          <span className={styles.sub}>default model {a.default_model}</span>
        ) : (
          <span className={styles.fix}>
            Add <code>{a.credential?.name}=…</code> to <code>.env</code> and restart. Other agents still work.
          </span>
        )}
      </div>
    </div>
  );
}

// AgentModels is one agent's model list, as the New task picker offers it.
// Collapsed to a one-line summary: an agent can offer dozens.
function AgentModels({ agent, requireZDR }: { agent: Agent; requireZDR: boolean }) {
  const { data, error } = useModels(agent.id);
  const zdr = data?.models.filter((m) => m.zdr).length ?? 0;
  return (
    <details className={`${styles.card} ${styles.models}`}>
      <summary className={styles.cardHead}>
        <span className="mono">{agent.id}</span>
        {data && (
          <span className={styles.sub}>
            {data.models.length} model{data.models.length === 1 ? "" : "s"} · {zdr} with ZDR · default {data.default}
          </span>
        )}
      </summary>
      {error && <p className={styles.badText}>Couldn't load models: {error.message}</p>}
      {data && (
        <div className={styles.tableWrap}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th scope="col">model</th>
                <th scope="col">effort</th>
                <th scope="col">thinking</th>
                <th scope="col">fast</th>
                <th scope="col">ZDR</th>
              </tr>
            </thead>
            <tbody>
              {data.models.map((m) => (
                <tr key={m.id} className={requireZDR && !m.zdr ? styles.refused : ""}>
                  <td>
                    <span className="mono">{m.id}</span>
                    {m.name !== m.id && <span className={styles.sub}> {m.name}</span>}
                    {m.id === data.default && <span className={`${styles.tag} ${styles.good}`}> DEFAULT</span>}
                  </td>
                  <td className="mono">
                    {m.efforts.length === 0
                      ? "model decides"
                      : m.efforts.map((e) => (e === m.default_effort ? `${e}*` : e)).join(" · ")}
                  </td>
                  <td className="mono">{m.thinking ? "yes" : "—"}</td>
                  <td className="mono">{m.fast ? "yes" : "—"}</td>
                  <td className={`mono ${m.zdr ? styles.goodText : styles.badText}`}>{m.zdr ? "yes" : "no"}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {data.models.some((m) => m.default_effort) && <p className={styles.note}>* the default effort</p>}
        </div>
      )}
    </details>
  );
}

function PullRequests({ status }: { status: Status }) {
  const prs = status.pull_requests;
  if (!prs.enabled) {
    return (
      <div className={styles.facts}>
        <span className={`${styles.tag} ${styles.bad}`}>OFF</span>
        <span>
          Tasks can't push their changes. Add <code>GITHUB_TOKEN=…</code> to <code>.env</code> and restart.
        </span>
      </div>
    );
  }
  return (
    <div className={styles.facts}>
      <span className={`${styles.tag} ${styles.good}`}>ON</span>
      <span>
        When you press push, a thread's changes go to a <code>forgerun/…</code> branch as <strong>{prs.github_user}</strong>, as one
        commit, with a pull request if you ask. Repos must be on github.com and writable by the token.
      </span>
    </div>
  );
}

function Sessions({ status }: { status: Status }) {
  const s = status.sessions;
  return (
    <div className={styles.facts}>
      <span className="mono">
        {s.open} open{s.max > 0 ? ` of ${s.max}` : ""}
      </span>
      <span>
        Each thread keeps its VM between messages, so the agent remembers the conversation.{" "}
        {s.idle_seconds > 0 ? `A session closes after ${formatDuration(s.idle_seconds)} without a message` : "Sessions stay open until ended"}
        {s.max > 0 ? `; past ${s.max} open, the least recently used idle one closes` : ""}. Unpushed work is kept: the next
        message reopens the session in a few seconds.
      </span>
    </div>
  );
}

function Snapshots({ status }: { status: Status }) {
  const { data: snaps } = useSnapshots();
  const s = status.snapshots;
  if (!s.enabled) {
    return (
      <div className={styles.card}>
        <div className={styles.facts}>
          <span className={`${styles.tag} ${styles.bad}`}>OFF</span>
          <span>Every task starts cold (a fresh clone). Start the server with snapshots on to reuse prepared VMs.</span>
        </div>
      </div>
    );
  }
  const pct = s.budget_bytes > 0 ? Math.min(100, (s.bytes / s.budget_bytes) * 100) : 0;
  return (
    <>
      <div className={styles.card}>
        <div className={styles.usage}>
          <span className="mono">
            {formatBytes(s.bytes)} used{s.budget_bytes > 0 ? ` of ${formatBytes(s.budget_bytes)}` : " (no limit)"} ·{" "}
            {s.count} snapshot{s.count === 1 ? "" : "s"}
          </span>
          {s.budget_bytes > 0 && (
            <div
              className={styles.bar}
              role="meter"
              aria-label="Snapshot disk used"
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={Math.round(pct)}
            >
              <div className={styles.barFill} style={{ width: `${pct}%` }} />
            </div>
          )}
        </div>
        <p className={styles.note}>
          A snapshot is a VM saved after a task, with the repo cloned and the agent set up, so the next task on that repo
          and agent starts warm.{" "}
          {s.max_age_seconds > 0
            ? `Snapshots unused for ${formatDuration(s.max_age_seconds)} are deleted`
            : "Unused snapshots are kept however old"}
          {s.budget_bytes > 0 ? "; over the budget, the least recently used go first" : ""}. Checked every{" "}
          {formatDuration(s.sweep_seconds)}.
        </p>
      </div>
      {snaps && snaps.length > 0 ? (
        <div className={`${styles.card} ${styles.tableWrap}`}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th scope="col">repo</th>
                <th scope="col">agent</th>
                <th scope="col">commit</th>
                <th scope="col">size</th>
                <th scope="col">last used</th>
              </tr>
            </thead>
            <tbody>{snaps.map((sn) => <SnapshotRow key={sn.id} snap={sn} />)}</tbody>
          </table>
        </div>
      ) : (
        snaps && <p className={styles.note}>No snapshots yet: the first task on a repo makes one.</p>
      )}
    </>
  );
}

function SnapshotRow({ snap }: { snap: Snapshot }) {
  return (
    <tr>
      <td>
        <span className="mono">{repoName(snap.repo)}</span>
        {snap.state === "retired" && <span className={`${styles.tag} ${styles.dim}`}> RETIRED</span>}
      </td>
      <td className="mono">{snap.agent}</td>
      <td className="mono">{snap.commit.slice(0, 7)}</td>
      <td className="mono">{formatBytes(snap.size_bytes)}</td>
      <td className="mono">{formatWhen(snap.last_used_at)}</td>
    </tr>
  );
}
