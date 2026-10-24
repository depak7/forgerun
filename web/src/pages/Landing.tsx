import { useState } from "react";
import styles from "./Landing.module.css";

const repoURL = "https://github.com/depak7/forgerun";
const installCmd = "curl -fsSL https://raw.githubusercontent.com/depak7/forgerun/main/install.sh | sh";

// LandingPage is what someone sees before signing in: what forgerun is, how
// it keeps their code and keys safe, what signing in grants, and the button.
// GitHub sends them back to /auth/github/callback.
export function LandingPage() {
  const error = new URLSearchParams(window.location.search).get("signin_error");
  return (
    <div className={styles.page}>
      <header className={styles.top}>
        <div className={styles.brand}>
          <span>&gt;_</span> forgerun
        </div>
        <nav className={styles.topLinks}>
          <a href={repoURL}>GitHub</a>
          <a className={styles.topSignIn} href="/auth/github/login">
            [ sign in ]
          </a>
        </nav>
      </header>

      <main className={styles.main}>
        <section className={styles.hero}>
          <span className={styles.eyebrow}>OPEN SOURCE · APACHE-2.0</span>
          <h1 className={styles.title}>Coding agents, each in its own microVM</h1>
          <p className={styles.lede}>
            forgerun is open-source infrastructure that runs Claude Code, Cursor CLI and OpenCode on your repo, each in its own
            microVM, on your own machines. It works in a private VM you can keep talking to, and nothing reaches your repo until
            you push.
          </p>
          <div className={styles.actions}>
            <a className={styles.button} href="/auth/github/login">
              [ sign in with GitHub ]
            </a>
            <a className={styles.buttonGhost} href={repoURL}>
              [ view on GitHub ]
            </a>
          </div>
          {error && (
            <p role="alert" className={styles.error}>
              Sign-in didn't work: {error}
            </p>
          )}
        </section>

        <section className={styles.section} aria-labelledby="how">
          <h2 id="how" className={styles.heading}>
            How it works
          </h2>
          <ol className={styles.steps}>
            <li className={styles.card}>
              <span className={styles.num}>01</span>
              <h3>Give it a task</h3>
              <p>Pick a repo, an agent and a model, and say what you want. Or ask for a plan first and approve it.</p>
            </li>
            <li className={styles.card}>
              <span className={styles.num}>02</span>
              <h3>It works in a microVM</h3>
              <p>
                The agent installs, builds and tests in a VM of its own. The VM stays up between your messages, so you can ask for
                changes and it remembers the conversation.
              </p>
            </li>
            <li className={styles.card}>
              <span className={styles.num}>03</span>
              <h3>Review, then push</h3>
              <p>Read each turn's diff. When you're happy, push it as one commit on a branch, and open a pull request if you want.</p>
            </li>
          </ol>
        </section>

        <section className={styles.section} aria-labelledby="safe">
          <h2 id="safe" className={styles.heading}>
            Why it's safe to point at your code
          </h2>
          <ul className={styles.points}>
            <li>
              <h3>One sandbox per thread</h3>
              <p>
                Each thread runs in its own microVM, or on Linux without KVM in a gVisor sandbox with its own kernel. What an agent
                runs stays there.
              </p>
            </li>
            <li>
              <h3>Keys never enter the sandbox</h3>
              <p>It sees only placeholders. A proxy outside it adds the real API key, and only for the agent's own API.</p>
            </li>
            <li>
              <h3>Nothing reaches GitHub until you push</h3>
              <p>Agents can't push. Changes leave the sandbox as a diff, and only your push turns them into a commit.</p>
            </li>
          </ul>
        </section>

        <section className={styles.section} aria-labelledby="agents">
          <h2 id="agents" className={styles.heading}>
            Agents
          </h2>
          <div className={styles.agents}>
            <div className={styles.agent}>
              <span className="mono">cursor</span>
              <span>Cursor CLI, with its model list</span>
            </div>
            <div className={styles.agent}>
              <span className="mono">claude-code</span>
              <span>Claude Code: Sonnet, Opus, Fable</span>
            </div>
            <div className={styles.agent}>
              <span className="mono">opencode</span>
              <span>OpenCode, including free models that need no key</span>
            </div>
          </div>
          <p className={styles.note}>Agents are adapters: adding another is one Go file and a list of models.</p>
        </section>

        <section className={styles.section} aria-labelledby="signin">
          <h2 id="signin" className={styles.heading}>
            What signing in means
          </h2>
          <ul className={styles.facts}>
            <li>
              forgerun asks GitHub for access to your repositories, so it can clone private ones and push branches and pull requests
              as you.
            </li>
            <li>Your GitHub token is stored encrypted, and your threads are visible only to you.</li>
            <li>There's a daily limit on how many turns each person can start.</li>
            <li>You can revoke access any time in GitHub → Settings → Applications.</li>
          </ul>
        </section>

        <section className={styles.section} aria-labelledby="self-host">
          <h2 id="self-host" className={styles.heading}>
            Run it yourself
          </h2>
          <p className={styles.text}>
            One command sets it up, on a Mac with Apple Silicon or a Linux machine (with KVM for microVMs; without it, it sets up
            gVisor):
          </p>
          <InstallCommand />
          <p className={styles.text}>
            It checks your machine, installs what's missing (Go, git and the rest), builds forgerun, runs it in the background, and
            asks what this machine is for:
          </p>
          <div className={styles.choices}>
            <div className={styles.card}>
              <h3>Everything here</h3>
              <p>The web UI, the database and the VMs, on one Mac or Linux box.</p>
            </div>
            <div className={styles.card}>
              <h3>A worker</h3>
              <p>The VMs run on your machine, connected to a forgerun server somewhere else. It dials out, so nothing needs opening.</p>
            </div>
          </div>
          <p className={styles.text}>
            Something not working? Run <code className={styles.inline}>forgerun doctor</code>: it says what's wrong and how to fix it.
          </p>
          <a className={styles.link} href={`${repoURL}#by-hand`}>
            Set it up by hand →
          </a>
        </section>
      </main>

      <footer className={styles.footer}>
        <span>forgerun · Apache-2.0</span>
        <a href={repoURL}>github.com/depak7/forgerun</a>
      </footer>
    </div>
  );
}

// InstallCommand is the one-line install, with a button that copies it.
function InstallCommand() {
  const [copied, setCopied] = useState(false);
  return (
    <div className={styles.install}>
      <pre className={styles.code}>
        <code>{installCmd}</code>
      </pre>
      <button
        type="button"
        className={styles.copy}
        onClick={() => {
          navigator.clipboard?.writeText(installCmd).then(
            () => {
              setCopied(true);
              setTimeout(() => setCopied(false), 2000);
            },
            () => {},
          );
        }}
      >
        {copied ? "[ copied ]" : "[ copy ]"}
      </button>
    </div>
  );
}
