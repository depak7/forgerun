import { useEffect, useRef, useState } from "react";
import styles from "./Landing.module.css";

const repoURL = "https://github.com/depak7/forgerun";
const installCmd = "curl -fsSL https://raw.githubusercontent.com/depak7/forgerun/main/install.sh | sh";

// LandingPage is forgerun's public page (FORGERUN_LANDING_ONLY): what it is,
// how it keeps code and keys safe, and how to run it yourself.
export function LandingPage() {
  return (
    <div className={styles.page}>
      <header className={styles.top}>
        <div className={styles.brand}>
          <span>&gt;_</span> forgerun
        </div>
        <nav className={styles.topLinks}>
          <a href="#how" className={styles.topNavHideMobile}>
            How it works
          </a>
          <a href="#compare" className={styles.topNavHideMobile}>
            Compare
          </a>
          <a href="#faq" className={styles.topNavHideMobile}>
            FAQ
          </a>
          <a className={styles.starPill} href={repoURL} target="_blank" rel="noreferrer">
            <StarIcon />
            <span>Star on GitHub</span>
          </a>
        </nav>
      </header>

      <main className={styles.main}>
        <section className={styles.hero}>
          <span className={styles.eyebrow}>OPEN SOURCE · APACHE-2.0 · SELF-HOSTED</span>
          <h1 className={styles.title}>Stop paying for sandboxes. Run coding agents on your own VM.</h1>
          <p className={styles.lede}>
            forgerun runs Claude Code, Cursor CLI, OpenCode, pi and Prime Agent on your repo, every turn in its own sandbox: a
            microVM, or gVisor on VMs without KVM. Drive it from another agent with <code className={styles.inline}>forgerun mcp</code>.
            Bring any Linux server or a Mac, and nothing reaches your repo until you push.
          </p>
          <div className={styles.try} role="group" aria-labelledby="try">
            <span id="try" className={styles.tryLabel}>
              TRY IT YOURSELF · ONE COMMAND
            </span>
            <InstallCommand />
            <span className={styles.tryNote}>On any Linux VM or an Apple Silicon Mac. Then open the web UI it prints.</span>
          </div>
          <div className={styles.actions}>
            <a className={styles.buttonGhost} href={repoURL}>
              <StarIcon />
              <span>[ view on GitHub ]</span>
            </a>
          </div>
        </section>

        <DemoVideo />

        <section className={styles.metrics} aria-label="Key benchmarks">
          <div className={styles.metric}>
            <span className={styles.metricVal}>26.8s → 2.8s</span>
            <span className={styles.metricDesc}>Warm task start from a repo disk snapshot with dependencies installed</span>
          </div>
          <div className={styles.metric}>
            <span className={styles.metricVal}>0.34s</span>
            <span className={styles.metricDesc}>gVisor user-space kernel cold boot on standard Linux VMs without KVM</span>
          </div>
          <div className={styles.metric}>
            <span className={styles.metricVal}>0 keys in VM</span>
            <span className={styles.metricDesc}>Sandboxes see only placeholders; a host TLS proxy swaps keys per API host</span>
          </div>
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
              <span>Cursor CLI, with its model list, thinking modes and effort levels</span>
            </div>
            <div className={styles.agent}>
              <span className="mono">claude-code</span>
              <span>Claude Code: Sonnet, Opus, Fable (runs on your Claude Pro or Max subscription)</span>
            </div>
            <div className={styles.agent}>
              <span className="mono">opencode</span>
              <span>OpenCode, including free models that need no key</span>
            </div>
            <div className={styles.agent}>
              <span className="mono">pi</span>
              <span>pi coding agent across OpenRouter, OpenAI and Anthropic keys</span>
            </div>
            <div className={styles.agent}>
              <span className="mono">prime</span>
              <span>Prime Agent: those providers plus Gemini, xAI, and Prime Inference</span>
            </div>
          </div>
          <p className={styles.note}>
            Agents are adapters: adding another is one Go file and a list of models. Another agent can drive forgerun with{" "}
            <code className={styles.inline}>forgerun mcp</code>.
          </p>
        </section>

        <section className={styles.section} aria-labelledby="compare">
          <h2 id="compare" className={styles.heading}>
            How it compares
          </h2>
          <div className={styles.tableWrap}>
            <table className={styles.compareTable}>
              <thead>
                <tr>
                  <th scope="col">Setup</th>
                  <th scope="col">Isolation per thread</th>
                  <th scope="col">API keys &amp; GitHub tokens</th>
                  <th scope="col">Task start &amp; workflow</th>
                  <th scope="col">Agents &amp; cost</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>
                    <strong className="mono">forgerun</strong> (your Mac or Linux VM)
                  </td>
                  <td>Dedicated microVM (libkrun) or gVisor kernel (0.34s boot)</td>
                  <td>Never inside the sandbox: host TLS proxy swaps placeholders</td>
                  <td>2.8s warm disk snapshots; review diff before pushing branch/PR</td>
                  <td>claude-code (Pro/Max), cursor, opencode (free/paid), pi, prime</td>
                </tr>
                <tr>
                  <td>CLI on your laptop</td>
                  <td>Shares your machine; stops when you close the lid</td>
                  <td>Local keys, ~/.ssh, and .env files live on the same machine</td>
                  <td>Ties up your local working tree and branch</td>
                  <td>Your own subscriptions, one terminal at a time</td>
                </tr>
                <tr>
                  <td>CLI in tmux or plain Docker on a VPS</td>
                  <td>Shared host kernel; containers share host or clone cold</td>
                  <td>Exported in env vars (readable by prompt injection or scripts)</td>
                  <td>~27s cold clone + install per clean run; no diff gate</td>
                  <td>Your own keys; DIY scripts for logs and PRs</td>
                </tr>
                <tr>
                  <td>Vendor cloud coding agents (Cursor Cloud Agents, Claude Code web, Codex Cloud, Copilot, Devin)</td>
                  <td>Vendor cloud VM (shared host on self-hosted runners)</td>
                  <td>Managed in vendor cloud; env vars often visible to the agent</td>
                  <td>Vendor web/editor tasks and cloud environments</td>
                  <td>Locked to one vendor&apos;s agent and seat/usage billing</td>
                </tr>
                <tr>
                  <td>Single-agent open-source runners (OpenHands)</td>
                  <td>Docker containers on your machine or cloud</td>
                  <td>Keys typically passed in config or container env</td>
                  <td>Web UI for its own built-in agent loop</td>
                  <td>Runs its own agent (not Claude Code, Cursor CLI, OpenCode, pi, Prime side by side)</td>
                </tr>
                <tr>
                  <td>Cloud sandbox providers &amp; SDKs (E2B, Daytona, Modal, Sprites, microsandbox)</td>
                  <td>Paid cloud microVMs or low-level runtime SDK</td>
                  <td>Varies (microsandbox brokers secrets; cloud sandboxes typically use env vars)</td>
                  <td>Low-level primitive — you build queue, UI, diff review, PR flow, and workers</td>
                  <td>Per-second cloud VM billing or DIY orchestration code</td>
                </tr>
              </tbody>
            </table>
          </div>
          <p className={styles.note}>
            Warm disk snapshots cut task start from <strong>26.8s to 2.8s</strong> on <code className={styles.inline}>expressjs/express</code>{" "}
            (<a className={styles.inlineLink} href={`${repoURL}/blob/main/docs/experiments.md`}>read the benchmarks →</a>).
          </p>
        </section>

        <section className={styles.section} aria-labelledby="self-host">
          <h2 id="self-host" className={styles.heading}>
            Run it yourself
          </h2>
          <p className={styles.text}>
            The command at the top works on a Mac with Apple Silicon or a Linux machine (with KVM for microVMs; without it, it sets
            up gVisor). It checks your machine, installs what's missing (Go, git and the rest), builds forgerun, runs it in the
            background, and asks what this machine is for:
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

        <section className={styles.section} aria-labelledby="faq">
          <h2 id="faq" className={styles.heading}>
            Frequently asked questions
          </h2>
          <div className={styles.faqs}>
            <details className={styles.faqItem}>
              <summary className={styles.faqQuestion}>
                How is forgerun different from Cursor Cloud Agents, Claude Code on the web, Codex Cloud, Devin, or OpenHands?
              </summary>
              <p className={styles.faqAnswer}>
                Cursor Cloud Agents, Claude Code on the web, Codex Cloud, GitHub Copilot coding agent, and Devin run in vendor-managed
                clouds and lock you to one vendor&apos;s agent and billing. OpenHands is open source, but runs its own single built-in
                agent in Docker. forgerun is 100% open source (Apache-2.0) and turns machines you already own into managed microVM or
                gVisor sandboxes where you can queue tasks across Claude Code, Cursor CLI, OpenCode, pi, and Prime Agent in one place,
                hand off work mid-task via <code className={styles.inline}>forgerun mcp</code>, start in 2.8s from rolling snapshots,
                and push PRs as you only when you want to.
              </p>
            </details>
            <details className={styles.faqItem}>
              <summary className={styles.faqQuestion}>
                How is forgerun different from cloud sandbox providers like E2B, Daytona, Modal, or Fly.io Sprites?
              </summary>
              <p className={styles.faqAnswer}>
                Those sell raw cloud sandbox VMs by the second (or give you a low-level SDK where you still have to build the agent
                runner yourself). forgerun uses your own Mac or Linux machines as the sandbox host (built on{" "}
                <code className={styles.inline}>microsandbox</code> and <code className={styles.inline}>gVisor</code>) and gives you the
                complete end-to-end platform out of the box: Postgres task queue, five agent adapters, automatic rolling snapshots,
                unified diff review, host-side branch and PR publishing, and <code className={styles.inline}>forgerun mcp</code>.
              </p>
            </details>
            <details className={styles.faqItem}>
              <summary className={styles.faqQuestion}>
                Can I queue multiple coding-agent tasks at once across different repos and agents?
              </summary>
              <p className={styles.faqAnswer}>
                Yes. Tasks sit in a Postgres queue, so you can queue as many as you want across repos, branches, and all five agents
                — even while workers are offline. Connected workers claim queued turns automatically as slots free up, each in its
                own isolated sandbox, and nothing is pushed until you review the diff and click push.
              </p>
            </details>
            <details className={styles.faqItem}>
              <summary className={styles.faqQuestion}>
                Can I hand off an in-progress local session mid-task to a cloud coding agent?
              </summary>
              <p className={styles.faqAnswer}>
                Yes. Connect <code className={styles.inline}>forgerun mcp</code> to Claude Code, Cursor, or Codex and ask it to hand
                the rest off (<code className={styles.inline}>forgerun_handoff</code>). It sends your repo or folder (uncommitted
                work included, <code className={styles.inline}>.env</code> and key files stripped) with a session summary to a
                sandboxed cloud thread so you can move on and push the PR later if you want it.
              </p>
            </details>
            <details className={styles.faqItem}>
              <summary className={styles.faqQuestion}>
                How do I run Claude Code (--dangerously-skip-permissions) or Cursor CLI safely in a microVM?
              </summary>
              <p className={styles.faqAnswer}>
                Use forgerun: each thread gets its own microsandbox microVM (or gVisor on VMs without KVM). Claude Code runs with{" "}
                <code className={styles.inline}>IS_SANDBOX=1</code>, <code className={styles.inline}>bypassPermissions</code> (or{" "}
                <code className={styles.inline}>plan</code> mode), and{" "}
                <code className={styles.inline}>ENABLE_CLAUDEAI_MCP_SERVERS=false</code> so personal connectors stay off. Keys never
                enter the sandbox, and nothing reaches GitHub until you review the diff and press push.
              </p>
            </details>
            <details className={styles.faqItem}>
              <summary className={styles.faqQuestion}>Can I use my existing Claude Pro or Max subscription?</summary>
              <p className={styles.faqAnswer}>
                Yes. Run <code className={styles.inline}>claude setup-token</code> on any machine with Claude Code installed and give the
                token to the installer as <code className={styles.inline}>CLAUDE_CODE_OAUTH_TOKEN</code>. Turns run against your
                subscription limits instead of pay-per-token API billing, and the token stays outside the sandbox behind the host proxy.
              </p>
            </details>
            <details className={styles.faqItem}>
              <summary className={styles.faqQuestion}>Does my Linux VPS need KVM or nested virtualization?</summary>
              <p className={styles.faqAnswer}>
                No. Where KVM (<code className={styles.inline}>/dev/kvm</code>) or Apple Silicon is available, forgerun boots a
                hardware-isolated microVM (<code className={styles.inline}>libkrun</code>). On standard cloud VMs without KVM, the
                installer automatically configures Docker with <code className={styles.inline}>gVisor</code> (
                <code className={styles.inline}>runsc</code>) so every thread still gets its own isolated user-space kernel (0.34s cold
                boot).
              </p>
            </details>
            <details className={styles.faqItem}>
              <summary className={styles.faqQuestion}>What happens if a prompt injection or dependency script tries to read my keys?</summary>
              <p className={styles.faqAnswer}>
                Inside the sandbox, environment variables hold only inert placeholders (<code className={styles.inline}>$MSB_…</code> or{" "}
                <code className={styles.inline}>FORGERUN_PH_…</code>). A TLS proxy on the host swaps the placeholder for the real key
                only on outbound requests to that agent&apos;s official API host. Sending the placeholder to any other host is blocked,
                and GitHub push tokens never enter the sandbox at all.
              </p>
            </details>
            <details className={styles.faqItem}>
              <summary className={styles.faqQuestion}>Can I host the web UI on a cheap VPS and run the VMs on my Mac at home?</summary>
              <p className={styles.faqAnswer}>
                Yes. Run <code className={styles.inline}>forgerun server</code> on a small 1 GB VPS and{" "}
                <code className={styles.inline}>forgerun worker</code> on your Mac or home Linux box. Workers dial out over HTTPS to
                claim turns, stream logs, and return diffs — zero inbound ports need to be opened on your home network.
              </p>
            </details>
          </div>
        </section>

        <section className={styles.starBanner} aria-label="Star on GitHub">
          <div className={styles.starBannerText}>
            <h2 className={styles.starBannerTitle}>Free, open-source (Apache-2.0), and built to self-host.</h2>
            <p className={styles.starBannerSub}>
              Star <code className={styles.inline}>depak7/forgerun</code> on GitHub to bookmark it, follow new agent adapters, or inspect
              the code.
            </p>
          </div>
          <a className={styles.starBannerBtn} href={repoURL} target="_blank" rel="noreferrer">
            <StarIcon />
            <span>[ star on GitHub ]</span>
          </a>
        </section>
      </main>

      <footer className={styles.footer}>
        <span>forgerun · Apache-2.0</span>
        <div className={styles.footerLinks}>
          <a href={`${repoURL}/blob/main/docs/deploying.md`}>Deploying</a>
          <a href={`${repoURL}/blob/main/docs/experiments.md`}>Benchmarks</a>
          <a href="/llms.txt">llms.txt</a>
          <a href={repoURL}>github.com/depak7/forgerun</a>
        </div>
      </footer>
    </div>
  );
}

// DemoVideo is the 30-second demo, right under the hero, with the browser's
// own controls to pause, replay and turn the sound up. It starts by itself
// with sound where the browser allows that, and muted where it doesn't (most
// do until the visitor has clicked on the page). With reduced motion asked
// for, it waits for a click instead.
function DemoVideo() {
  const ref = useRef<HTMLVideoElement>(null);
  const still = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
  useEffect(() => {
    const video = ref.current;
    if (!video || still) return;
    video.muted = false;
    video.play()?.catch(() => {
      video.muted = true;
      video.play()?.catch(() => {});
    });
  }, [still]);
  return (
    <section className={styles.demo} aria-label="Demo video">
      <video
        ref={ref}
        className={styles.video}
        src="/demo/forgerun-demo.mp4"
        poster="/demo/poster.jpg"
        controls
        playsInline
        preload={still ? "none" : "auto"}
        width={1920}
        height={1080}
      />
      <div className={styles.demoBar}>
        <span>30 seconds · a real run on forgerun</span>
      </div>
    </section>
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

function StarIcon() {
  return (
    <svg
      className={styles.starIcon}
      width={15}
      height={15}
      viewBox="0 0 16 16"
      fill="currentColor"
      aria-hidden="true"
    >
      <path d="M8 .25a.75.75 0 0 1 .673.418l1.882 3.815 4.21.612a.75.75 0 0 1 .416 1.279l-3.046 2.97.719 4.192a.751.751 0 0 1-1.088.791L8 12.347l-3.766 1.98a.75.75 0 0 1-1.088-.79l.72-4.194L.818 6.374a.75.75 0 0 1 .416-1.28l4.21-.611L7.327.668A.75.75 0 0 1 8 .25Z" />
    </svg>
  );
}
