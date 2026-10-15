import { NavLink, Outlet } from "react-router-dom";
import { useAgents, useStatus } from "../api/queries";
import { formatBytes } from "../format";
import styles from "./Layout.module.css";

// The frame around every page: the status strip, the sidebar, and the page
// itself (<Outlet />, filled in by the router).
export function Layout() {
  return (
    <div className={styles.app}>
      <StatusStrip />
      <div className={styles.body}>
        <Sidebar />
        <main className={styles.main}>
          <Outlet />
        </main>
      </div>
    </div>
  );
}

function StatusStrip() {
  const { data: status, error } = useStatus();
  if (error) {
    return (
      <div className={styles.strip} role="status">
        <span className="missing">CAN'T REACH THE SERVER: {error.message}</span>
      </div>
    );
  }
  const running = status?.tasks.running ?? 0;
  const queued = status?.tasks.queued ?? 0;
  return (
    <div className={styles.strip} aria-label="Agents at work">
      {running + queued > 0 ? (
        <span className={styles.alive}>
          <span className="live">●</span> {running} RUNNING · {queued} QUEUED
        </span>
      ) : (
        <span>NO AGENTS RUNNING</span>
      )}
      {status?.snapshots.enabled && (
        <span className={styles.stripRight}>
          SNAPSHOTS {formatBytes(status.snapshots.bytes)}
          {status.snapshots.budget_bytes > 0 && ` / ${formatBytes(status.snapshots.budget_bytes)}`}
        </span>
      )}
    </div>
  );
}

function Sidebar() {
  const { data: agents } = useAgents();
  const { data: status } = useStatus();
  const linkClass = ({ isActive }: { isActive: boolean }) => (isActive ? `${styles.link} ${styles.linkActive}` : styles.link);
  return (
    <nav className={styles.nav} aria-label="Main">
      <NavLink to="/" className={styles.brand}>
        <span>&gt;_</span>
        <span>forgerun</span>
      </NavLink>
      <div className={styles.links}>
        <NavLink to="/" end className={linkClass}>
          tasks
        </NavLink>
        <NavLink to="/settings" className={linkClass}>
          settings
        </NavLink>
      </div>
      <div className={styles.creds}>
        <span className="label">Credentials</span>
        {agents
          ?.filter((a) => a.credential?.name)
          .map((a) => (
            <span key={a.id} className={styles.credRow}>
              <span>{a.id}</span>
              <span className={a.credential?.set ? styles.set : styles.missing}>{a.credential?.set ? "SET" : "MISSING"}</span>
            </span>
          ))}
        <span className={styles.credRow}>
          <span>github</span>
          <span>{status?.pull_requests.enabled ? status.pull_requests.github_user : <span className={styles.missing}>OFF</span>}</span>
        </span>
      </div>
    </nav>
  );
}
