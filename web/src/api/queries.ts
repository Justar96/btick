import { queryOptions } from "@tanstack/react-query";
import { api } from "./client";

export interface SymbolMetadata {
  symbol: string;
  base_asset: string;
  quote_asset: string;
  product_type: string;
  product_sub_type: string;
  product_name: string;
  market_hours: string;
  feed_id: string;
}

function buildApiUrl(path: string, query?: Record<string, string | undefined>) {
  const baseUrl = import.meta.env.VITE_API_URL ?? "";
  const url = new URL(path, baseUrl || window.location.origin);
  if (query) {
    for (const [key, value] of Object.entries(query)) {
      if (value) {
        url.searchParams.set(key, value);
      }
    }
  }
  return baseUrl ? url.toString() : `${url.pathname}${url.search}`;
}

export function latestPriceOptions(symbol: string) {
  return queryOptions({
    queryKey: ["price", "latest", symbol],
    queryFn: async () => {
      const resp = await fetch(buildApiUrl("/v1/price/latest", { symbol }));
      if (!resp.ok) throw new Error("Failed to fetch latest price");
      return resp.json();
    },
    staleTime: 30_000,
  });
}

export function snapshotsOptions(symbol: string, start: string, end?: string) {
  return queryOptions({
    queryKey: ["price", "snapshots", symbol, start, end],
    queryFn: async () => {
      const { data, error } = await api.GET("/v1/price/snapshots", {
        params: { query: { start, end } },
      });
      if (error) throw error;
      // Backend returns all symbols; filter client-side
      return (data ?? []).filter((s: { symbol?: string }) => s.symbol === symbol);
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
      const resp = await fetch(buildApiUrl("/v1/health", { symbol }));
      if (!resp.ok) throw new Error("Failed to fetch health");
      return resp.json();
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
      const resp = await fetch(buildApiUrl("/v1/symbols"));
      if (!resp.ok) throw new Error("Failed to fetch symbols");
      return resp.json() as Promise<string[]>;
    },
    staleTime: 60_000,
  });
}

export function symbolMetadataOptions(symbol: string) {
  return queryOptions({
    queryKey: ["metadata", symbol],
    queryFn: async () => {
      const resp = await fetch(buildApiUrl("/v1/metadata", { symbol }));
      if (!resp.ok) throw new Error("Failed to fetch symbol metadata");
      return resp.json() as Promise<SymbolMetadata>;
    },
    staleTime: 5 * 60_000,
  });
}
