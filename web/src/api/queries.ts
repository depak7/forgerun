import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api } from "./client";
import type { Agent, Status, Thread } from "./types";

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
