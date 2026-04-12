import { queryOptions } from "@tanstack/react-query";
import { api } from "./client";

export function latestPriceOptions(symbol: string) {
  return queryOptions({
    queryKey: ["price", "latest", symbol],
    queryFn: async () => {
      const { data, error } = await api.GET("/v1/price/latest");
      if (error) throw error;
      return data;
    },
    staleTime: 30_000,
  });
}

export function snapshotsOptions(start: string, end?: string) {
  return queryOptions({
    queryKey: ["price", "snapshots", start, end],
    queryFn: async () => {
      const { data, error } = await api.GET("/v1/price/snapshots", {
        params: { query: { start, end } },
      });
      if (error) throw error;
      return data;
    },
    staleTime: Infinity,
  });
}

export function ticksOptions(limit?: number) {
  return queryOptions({
    queryKey: ["price", "ticks", limit],
    queryFn: async () => {
      const { data, error } = await api.GET("/v1/price/ticks", {
        params: { query: { limit } },
      });
      if (error) throw error;
      return data;
    },
    staleTime: 2000,
  });
}

export function healthOptions(symbol?: string) {
  return queryOptions({
    queryKey: ["health", symbol],
    queryFn: async () => {
      const { data, error } = await api.GET("/v1/health");
      if (error) throw error;
      return data;
    },
    staleTime: 3000,
  });
}

export function feedHealthOptions() {
  return queryOptions({
    queryKey: ["health", "feeds"],
    queryFn: async () => {
      const { data, error } = await api.GET("/v1/health/feeds");
      if (error) throw error;
      return data;
    },
    staleTime: 30_000,
  });
}

export function symbolsOptions() {
  return queryOptions({
    queryKey: ["symbols"],
    queryFn: async () => {
      const resp = await fetch(
        (import.meta.env.VITE_API_URL ?? "") + "/v1/symbols",
      );
      if (!resp.ok) throw new Error("Failed to fetch symbols");
      return resp.json() as Promise<string[]>;
    },
    staleTime: 60_000,
  });
}
