import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api } from "./client";
import type { Agent, ModelList, Session, Snapshot, Status, Task, Thread } from "./types";

// Hooks for the data screens share. TanStack Query caches each by its key,
// so the strip, sidebar and pages share one request, refreshed while the
// page is open.

export function useStatus() {
  return useQuery({ queryKey: ["status"], queryFn: () => api<Status>("/api/status"), refetchInterval: 3000 });
}

export function useAgents() {
  return useQuery({ queryKey: ["agents"], queryFn: () => api<Agent[]>("/api/agents"), refetchInterval: 15000 });
}

export function useThreads() {
  return useQuery({ queryKey: ["threads"], queryFn: () => api<Thread[]>("/api/threads?limit=100"), refetchInterval: 3000 });
}

export function useModels(agent: string) {
  return useQuery({
    queryKey: ["models", agent],
    queryFn: () => api<ModelList>(`/api/models?agent=${encodeURIComponent(agent)}`),
    enabled: agent !== "",
    staleTime: 60_000, // the model list rarely changes
  });
}

export function useSnapshots() {
  return useQuery({ queryKey: ["snapshots"], queryFn: () => api<Snapshot[]>("/api/snapshots"), staleTime: 30_000 });
}

// useThread is every step of a thread, oldest first: often while a step
// is waiting or running, rarely once it's all finished.
export function useThread(id: string) {
  return useQuery({
    queryKey: ["thread", id],
    queryFn: () => api<Task[]>(`/api/tasks/${encodeURIComponent(id)}/thread`),
    refetchInterval: (q) => (q.state.data?.some((t) => t.state === "queued" || t.state === "running") ? 2000 : 10000),
  });
}

// useSession is a thread's session, refreshed while the page is open.
export function useSession(threadId: string) {
  return useQuery({
    queryKey: ["session", threadId],
    queryFn: () => api<Session>(`/api/threads/${encodeURIComponent(threadId)}/session`),
    refetchInterval: 5000,
  });
}

// useDiff is a finished step's changes, as git diff text.
export function useDiff(taskId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["diff", taskId],
    enabled,
    staleTime: Infinity, // a finished step's diff never changes
    queryFn: async () => {
      const res = await fetch(`/api/tasks/${encodeURIComponent(taskId)}/diff`);
      if (res.status === 404) return "";
      if (!res.ok) throw new Error(`diff: HTTP ${res.status}`);
      return res.text();
    },
  });
}

// useLiveLog is a step's whole log, fetched incrementally: it remembers how
// many bytes it has and asks only for the rest, every 2 seconds while live,
// so a long log isn't downloaded again and again. It asks from one byte
// before the end it has (bytes=N-1-): the answer always holds at least that
// byte, so a quiet agent doesn't turn every poll into a 416 error in the
// browser console. That repeated byte is dropped.
export function useLiveLog(taskId: string, live: boolean): { text: string; error: string } {
  const [state, setState] = useState({ id: taskId, text: "", error: "" });
  useEffect(() => {
    let cancelled = false;
    let text = "";
    let bytes = 0; // bytes of the log we already have
    // One decoder in streaming mode: a character split across two chunks
    // is held back until its other half arrives, instead of garbled.
    let decoder = new TextDecoder();
    setState({ id: taskId, text: "", error: "" });
    const pull = async () => {
      const res = await fetch(`/api/tasks/${encodeURIComponent(taskId)}/log`, bytes > 0 ? { headers: { Range: `bytes=${bytes - 1}-` } } : {});
      if (cancelled) return;
      if (res.status === 416) return; // the log got shorter than we had: keep what we have
      if (res.status === 404) {
        setState({ id: taskId, text: "", error: "" }); // no log yet: still queued
        return;
      }
      if (!res.ok) {
        setState({ id: taskId, text, error: `log: HTTP ${res.status}` });
        return;
      }
      let chunk = new Uint8Array(await res.arrayBuffer());
      if (cancelled) return;
      if (res.status === 200 && bytes > 0) {
        text = ""; // the server sent it all again
        decoder = new TextDecoder();
      } else if (res.status === 206) {
        chunk = chunk.subarray(1); // the byte we already had
        if (chunk.length === 0) return; // nothing new yet
      }
      bytes = res.status === 200 ? chunk.length : bytes + chunk.length;
      text += decoder.decode(chunk, { stream: true });
      setState({ id: taskId, text, error: "" });
    };
    pull();
    const timer = live ? setInterval(pull, 2000) : undefined;
    return () => {
      cancelled = true;
      if (timer) clearInterval(timer);
    };
  }, [taskId, live]);
  return state.id === taskId ? state : { text: "", error: "" };
}

// useLogTail is the last line a running task's agent wrote. It asks for
// only the end of the log (a suffix Range request), every 2 seconds.
export function useLogTail(taskId: string, enabled: boolean) {
  return useQuery({
    queryKey: ["log-tail", taskId],
    queryFn: () => logTail(taskId),
    enabled,
    refetchInterval: enabled ? 2000 : false,
  });
}

export async function logTail(taskId: string, bytes = 400): Promise<string> {
  const res = await fetch(`/api/tasks/${encodeURIComponent(taskId)}/log`, { headers: { Range: `bytes=-${bytes}` } });
  if (!res.ok) return ""; // no log yet (queued) or not readable
  return lastLine(await res.text());
}

// lastLine is the last non-empty line of a chunk of log. The chunk may
// start mid-line, which is fine: only its end is used.
export function lastLine(text: string): string {
  const lines = text.split("\n").map((l) => l.trimEnd());
  for (let i = lines.length - 1; i >= 0; i--) {
    if (lines[i]) return lines[i];
  }
  return "";
}

// useNow re-renders every interval, for live timers like elapsed time.
export function useNow(intervalMs = 1000): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), intervalMs);
    return () => clearInterval(t);
  }, [intervalMs]);
  return now;
}
