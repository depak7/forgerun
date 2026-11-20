import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import styles from "./Login.module.css";

// LoginPage asks for the owner's username and password (the server's
// FORGERUN_USERNAME and FORGERUN_PASSWORD). Signed in, the app takes over.
export function LoginPage() {
  const queryClient = useQueryClient();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const login = useMutation({
    mutationFn: () => api<object>("/auth/login", { method: "POST", body: JSON.stringify({ username, password }) }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["me"] }),
  });
  return (
    <main className={styles.page}>
      <form
        className={styles.form}
        onSubmit={(e) => {
          e.preventDefault();
          login.mutate();
        }}
      >
        <div className={styles.brand}>
          <span>&gt;_</span> forgerun
        </div>
        <label className={styles.field}>
          <span className="label">Username</span>
          <input className={styles.input} autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} />
        </label>
        <label className={styles.field}>
          <span className="label">Password</span>
          <input
            className={styles.input}
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        {login.error && (
          <p role="alert" className={styles.error}>
            {login.error.message}
          </p>
        )}
        <button type="submit" className={styles.button} disabled={login.isPending || !username || !password}>
          [ sign in ]
        </button>
      </form>
    </main>
  );
}
