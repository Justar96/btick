import {
  createContext,
  useContext,
  useState,
  useCallback,
  type ReactNode,
} from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useWebSocket } from "./useWebSocket";
import type { WSMessage, PriceState, SourcePrice, SourceStatus } from "./types";

interface WSContextValue {
  prices: Record<string, PriceState>;
  connected: boolean;
  sourcePrices: Record<string, Record<string, SourcePrice>>;
  sourceStatus: Record<string, Record<string, SourceStatus>>;
}

const WSContext = createContext<WSContextValue>({
  prices: {},
  connected: false,
  sourcePrices: {},
  sourceStatus: {},
});

export function usePrice(symbol: string): PriceState | undefined {
  return useContext(WSContext).prices[symbol];
}

export function useWSConnected(): boolean {
  return useContext(WSContext).connected;
}

export function useSourcePrices(
  symbol: string,
): Record<string, SourcePrice> {
  return useContext(WSContext).sourcePrices[symbol] ?? {};
}

export function useSourceStatus(
  symbol: string,
): Record<string, SourceStatus> {
  return useContext(WSContext).sourceStatus[symbol] ?? {};
}

const MAX_LIVE_SNAPSHOTS = 3600;

interface SnapshotPoint {
  ts_second: string;
  price: string;
}

export function WebSocketProvider({ children }: { children: ReactNode }) {
  const queryClient = useQueryClient();
  const [connected, setConnected] = useState(false);
  const [prices, setPrices] = useState<Record<string, PriceState>>({});
  const [sourcePrices, setSourcePrices] = useState<
    Record<string, Record<string, SourcePrice>>
  >({});
  const [sourceStatus, setSourceStatus] = useState<
    Record<string, Record<string, SourceStatus>>
  >({});

  const wsUrl = (import.meta.env.VITE_API_URL ?? "")
    .replace(/^http/, "ws")
    + "/ws/price";

  const url = import.meta.env.VITE_API_URL
    ? wsUrl
    : `ws://${window.location.host}/ws/price`;

  const handleMessage = useCallback(
    (msg: WSMessage) => {
      if (msg.type === "latest_price" && msg.symbol && msg.price) {
        const state: PriceState = {
          symbol: msg.symbol,
          price: msg.price,
          ts: msg.ts ?? "",
          basis: msg.basis ?? "",
          isStale: msg.is_stale ?? false,
          isDegraded: msg.is_degraded ?? false,
          qualityScore: parseFloat(msg.quality_score ?? "0"),
          sourceCount: msg.source_count ?? 0,
          sourcesUsed: msg.sources_used ?? [],
        };

        setPrices((prev) => ({ ...prev, [msg.symbol!]: state }));

        // Bridge to TanStack Query cache
        queryClient.setQueryData(["price", "latest", msg.symbol], {
          symbol: msg.symbol,
          ts: msg.ts,
          price: msg.price,
          basis: msg.basis,
          is_stale: msg.is_stale,
          is_degraded: msg.is_degraded,
          quality_score: state.qualityScore,
          source_count: msg.source_count,
          sources_used: msg.sources_used,
        });
      }

      if (msg.type === "snapshot_1s" && msg.symbol && msg.price && msg.ts) {
        const point: SnapshotPoint = {
          ts_second: msg.ts,
          price: msg.price,
        };

        queryClient.setQueryData<SnapshotPoint[]>(
          ["price", "snapshots", "live", msg.symbol],
          (old) => {
            const arr = old ?? [];
            const updated = [...arr, point];
            if (updated.length > MAX_LIVE_SNAPSHOTS) {
              return updated.slice(updated.length - MAX_LIVE_SNAPSHOTS);
            }
            return updated;
          },
        );
      }

      if (msg.type === "source_price" && msg.symbol && msg.source && msg.price) {
        setSourcePrices((prev) => ({
          ...prev,
          [msg.symbol!]: {
            ...(prev[msg.symbol!] ?? {}),
            [msg.source!]: {
              source: msg.source!,
              price: msg.price!,
              ts: msg.ts ?? "",
            },
          },
        }));
      }

      if (msg.type === "source_status" && msg.symbol && msg.source) {
        setSourceStatus((prev) => ({
          ...prev,
          [msg.symbol!]: {
            ...(prev[msg.symbol!] ?? {}),
            [msg.source!]: {
              source: msg.source!,
              connState: msg.conn_state ?? "unknown",
              stale: msg.stale ?? false,
              ts: msg.ts ?? "",
            },
          },
        }));
      }
    },
    [queryClient],
  );

  const handleReconnect = useCallback(() => {
    queryClient.invalidateQueries({ queryKey: ["price", "snapshots"] });
  }, [queryClient]);

  useWebSocket({
    url,
    onMessage: handleMessage,
    onStatusChange: setConnected,
    onReconnect: handleReconnect,
  });

  return (
    <WSContext.Provider value={{ prices, connected, sourcePrices, sourceStatus }}>
      {children}
    </WSContext.Provider>
  );
}
