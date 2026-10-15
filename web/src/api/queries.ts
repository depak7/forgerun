import { useQuery } from "@tanstack/react-query";
import { api } from "./client";
import type { Agent, Status } from "./types";

// Hooks for data every screen shares. TanStack Query caches each by its key,
// so the strip, sidebar and pages share one request, refreshed every few
// seconds while the page is open.

export function useStatus() {
  return useQuery({ queryKey: ["status"], queryFn: () => api<Status>("/api/status"), refetchInterval: 3000 });
}

export function useAgents() {
  return useQuery({ queryKey: ["agents"], queryFn: () => api<Agent[]>("/api/agents"), refetchInterval: 15000 });
}
