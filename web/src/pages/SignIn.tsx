import styles from "./SignIn.module.css";

// SignInPage is what someone sees before signing in: what forgerun is, and
// one button. GitHub sends them back to /auth/github/callback.
export function SignInPage() {
  const error = new URLSearchParams(window.location.search).get("signin_error");
  return (
    <main className={styles.page}>
      <div className={styles.card}>
        <div className={styles.brand}>
          <span>&gt;_</span> forgerun
        </div>
        <h1 className={styles.title}>Coding agents in their own microVMs</h1>
        <p className={styles.lede}>
          Give an agent a repo and a task. It works in a private microVM you can keep talking to, and nothing reaches your repo
          until you push.
        </p>
        <a className={styles.button} href="/auth/github/login">
          [ sign in with GitHub ]
        </a>
        <p className={styles.note}>
          forgerun asks GitHub for access to your repositories, so it can clone them and push branches and pull requests for you.
          You can revoke it any time in GitHub's settings.
        </p>
        {error && (
          <p role="alert" className={styles.error}>
            Sign-in didn't work: {error}
          </p>
        )}
      </div>
    </main>
  );
}
