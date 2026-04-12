import { useState, useCallback, useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Liveline } from "liveline";
import type { LivelinePoint } from "liveline";
import { snapshotsOptions } from "@/api/queries";
import styles from "./PriceChart.module.css";

const WINDOWS = [
  { label: "1m", secs: 60 },
  { label: "5m", secs: 300 },
  { label: "1h", secs: 3600 },
];

interface Props {
  symbol: string;
}

interface SnapshotRow {
  ts_second: string;
  price: string;
}

export function PriceChart({ symbol }: Props) {
  const [windowSecs, setWindowSecs] = useState(300);

  // Compute time range for backfill query
  const start = useMemo(() => {
    const d = new Date(Date.now() - windowSecs * 1000);
    return d.toISOString();
  }, [windowSecs]);

  // Backfill from REST via TanStack Query
  const { data: backfillData } = useQuery(snapshotsOptions(start));

  // Live snapshots from WS (appended by WebSocketProvider via setQueryData)
  // useQuery subscribes to cache updates so the component re-renders on new points
  const { data: liveData } = useQuery<SnapshotRow[]>({
    queryKey: ["price", "snapshots", "live", symbol],
    queryFn: () => [],
    staleTime: Infinity,
    refetchOnMount: false,
    refetchOnWindowFocus: false,
  });

  // Merge backfill + live, deduplicate by rounded timestamp
  const merged = useMemo(() => {
    const backfillPoints: LivelinePoint[] = (backfillData ?? []).flatMap(
      (r) => {
        if (!r.ts_second || !r.price) return [];
        return [
          {
            time: new Date(r.ts_second).getTime() / 1000,
            value: parseFloat(r.price),
          },
        ];
      },
    );

    const livePoints: LivelinePoint[] = (liveData ?? []).map(
      (r: SnapshotRow) => ({
        time: new Date(r.ts_second).getTime() / 1000,
        value: parseFloat(r.price),
      }),
    );

    const seen = new Set<number>();
    const result: LivelinePoint[] = [];
    for (const pt of [...backfillPoints, ...livePoints]) {
      const key = Math.round(pt.time);
      if (!seen.has(key)) {
        seen.add(key);
        result.push(pt);
      }
    }
    result.sort((a, b) => a.time - b.time);
    return result;
  }, [backfillData, liveData]);

  const latest = merged.length > 0 ? merged[merged.length - 1].value : 0;

  // Compute range stats from visible data
  const range = useMemo(() => {
    if (merged.length === 0) return null;
    const now = Date.now() / 1000;
    const cutoff = now - windowSecs;
    const visible = merged.filter((p) => p.time >= cutoff);
    if (visible.length === 0) return null;

    let high = -Infinity;
    let low = Infinity;
    const open = visible[0].value;
    const close = visible[visible.length - 1].value;

    for (const p of visible) {
      if (p.value > high) high = p.value;
      if (p.value < low) low = p.value;
    }

    const change = close - open;
    const changePct = open !== 0 ? (change / open) * 100 : 0;

    return { high, low, open, close, change, changePct };
  }, [merged, windowSecs]);

  const handleWindowChange = useCallback((secs: number) => {
    setWindowSecs(secs);
  }, []);

  const fmt = (v: number) =>
    v.toLocaleString("en-US", {
      minimumFractionDigits: 2,
      maximumFractionDigits: 2,
    });

  return (
    <div id="chart" className={styles.wrap}>
      {merged.length > 0 ? (
        <>
          <Liveline
            data={merged}
            value={latest}
            window={windowSecs}
            windows={WINDOWS}
            windowStyle="rounded"
            onWindowChange={handleWindowChange}
            fill
            grid
            badge
            momentum
            showValue
            formatValue={fmt}
            style={{ height: 220 }}
          />
          {range && (
            <div className={styles.range}>
              <div className={styles.rangeStat}>
                <span className={styles.rangeLabel}>H</span>
                <span className={styles.rangeValue}>{fmt(range.high)}</span>
              </div>
              <div className={styles.rangeStat}>
                <span className={styles.rangeLabel}>L</span>
                <span className={styles.rangeValue}>{fmt(range.low)}</span>
              </div>
              <div className={styles.rangeStat}>
                <span className={styles.rangeLabel}>O</span>
                <span className={styles.rangeValue}>{fmt(range.open)}</span>
              </div>
              <div className={styles.rangeStat}>
                <span className={styles.rangeLabel}>C</span>
                <span className={styles.rangeValue}>{fmt(range.close)}</span>
              </div>
              <div className={styles.rangeStat}>
                <span className={styles.rangeLabel}>Chg</span>
                <span
                  className={`${styles.rangeValue} ${range.change >= 0 ? styles.rangeUp : styles.rangeDown}`}
                >
                  {range.change >= 0 ? "+" : ""}{fmt(range.change)}
                  <span className={styles.rangePct}>
                    {" "}({range.changePct >= 0 ? "+" : ""}{range.changePct.toFixed(2)}%)
                  </span>
                </span>
              </div>
            </div>
          )}
        </>
      ) : (
        <div className={styles.empty}>Waiting for data...</div>
      )}
    </div>
  );
}
