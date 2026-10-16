import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, NavLink, Outlet, useLocation } from "react-router-dom";
import { api } from "../api/client";
import { useAgents, useMe, useStatus, useThreads } from "../api/queries";
import type { Me } from "../api/types";
import { SignInPage } from "../pages/SignIn";
import { formatBytes, shortID } from "../format";
import styles from "./Layout.module.css";

// The frame around every page: the status strip, the sidebar, and the page
// itself (<Outlet />, filled in by the router).
export function Layout() {
  const { data: me } = useMe();
  if (!me) return null; // a moment, while we learn who's signed in
  if (!me.signed_in) return <SignInPage />;
  return (
    <div className={styles.app}>
      <StatusStrip />
      <div className={styles.body}>
        <Sidebar me={me} />
        <main className={styles.main}>
          <Outlet />
        </main>
      </div>
    </div>
  );
}

function StatusStrip() {
  const { data: status, error } = useStatus();
  const { data: threads } = useThreads();
  if (error) {
    return (
      <div className={styles.strip} role="status">
        <span className={styles.missing}>
          CAN'T REACH THE SERVER: {error.message}
        </span>
      </div>
    );
  }
  const running = status?.tasks.running ?? 0;
  const queued = status?.tasks.queued ?? 0;
  const live = (threads ?? [])
    .filter((t) => t.latest.state === "running" || t.latest.state === "queued")
    .slice(0, 3);
  return (
    <div className={styles.strip} aria-label="Agents at work">
      {running + queued > 0 ? (
        <span className={styles.alive}>
          <span className="live">●</span> {running} RUNNING · {queued} QUEUED
        </span>
      ) : (
        <span>NO AGENTS RUNNING</span>
      )}
      {live.map((t) => (
        <Link key={t.latest.id} to={`/threads/${t.id}`}>
          {shortID(t.latest.id).toUpperCase()} · {t.latest.agent.toUpperCase()}{" "}
          ·{" "}
          <span
            style={{
              color:
                t.latest.state === "running" ? "var(--running)" : "var(--text)",
            }}
          >
            {t.latest.state.toUpperCase()}
          </span>{" "}
          →
        </Link>
      ))}
      {status?.paused && <span className={styles.missing}>NEW TASKS PAUSED</span>}
      {status?.snapshots.enabled && (
        <span className={styles.stripRight}>
          SNAPSHOTS {formatBytes(status.snapshots.bytes)}
          {status.snapshots.budget_bytes > 0 &&
            ` / ${formatBytes(status.snapshots.budget_bytes)}`}
        </span>
      )}
    </div>
  );
}

function Sidebar({ me }: { me: Me }) {
  const { data: agents } = useAgents();
  const { data: status } = useStatus();
  const { pathname } = useLocation();
  // New task and thread pages belong to "tasks" in the menu.
  const inTasks =
    pathname === "/" ||
    pathname.startsWith("/tasks/") ||
    pathname.startsWith("/threads/");
  const linkClass = (active: boolean) =>
    active ? `${styles.link} ${styles.linkActive}` : styles.link;
  return (
    <nav className={styles.nav} aria-label="Main">
      <NavLink to="/" className={styles.brand}>
        <span>&gt;_</span>
        <span>forgerun</span>
      </NavLink>
      <div className={styles.links}>
        <NavLink
          to="/"
          className={linkClass(inTasks)}
          aria-current={inTasks ? "page" : undefined}
        >
          tasks
        </NavLink>
        {me.admin && (
          <NavLink
            to="/settings"
            className={({ isActive }) => linkClass(isActive)}
          >
            settings
          </NavLink>
        )}
      </div>
      {me.admin && (
        <div className={styles.creds}>
          <span className="label">Credentials</span>
          {agents
            ?.filter((a) => a.credential?.name)
            .map((a) => (
              <span key={a.id} className={styles.credRow}>
                <span>{a.id}</span>
                <span
                  className={a.credential?.set ? styles.set : styles.missing}
                >
                  {a.credential?.set ? "SET" : "MISSING"}
                </span>
              </span>
            ))}
          <span className={styles.credRow}>
            <span>github</span>
            <span>
              {status?.pull_requests.enabled ? (
                status.pull_requests.github_user
              ) : (
                <span className={styles.missing}>OFF</span>
              )}
            </span>
          </span>
        </div>
      )}
      {me.sign_in && <Account me={me} />}
    </nav>
  );
}

// Account is the signed-in user, with sign out.
function Account({ me }: { me: Me }) {
  const queryClient = useQueryClient();
  const signOut = useMutation({
    mutationFn: () => api<object>("/auth/logout", { method: "POST" }),
    onSuccess: () => queryClient.clear(),
  });
  return (
    <div className={`${styles.account} ${me.admin ? "" : styles.accountAlone}`}>
      {me.avatar_url && (
        <img
          src={me.avatar_url}
          alt=""
          width={24}
          height={24}
          className={styles.avatar}
        />
      )}
      <span className={styles.accountName} title={me.name || me.login}>
        {me.login}
        {me.admin && <span className={styles.adminTag}> admin</span>}
      </span>
      <button
        type="button"
        className={styles.signOut}
        disabled={signOut.isPending}
        onClick={() => signOut.mutate()}
      >
        sign out
      </button>
    </div>
  );
}
