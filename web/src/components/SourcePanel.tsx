import { useRef, useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useSourcePrices, useSourceStatus } from "@/ws/context";
import { feedHealthOptions } from "@/api/queries";
import type { PriceState } from "@/ws/types";
import styles from "./SourcePanel.module.css";

const EXCHANGES = ["binance", "coinbase", "kraken", "okx"] as const;

const EXCHANGE_META: Record<string, { color: string; abbr: string; label: string }> = {
  binance: { color: "#f0b90b", abbr: "BN", label: "Binance" },
  coinbase: { color: "#0052ff", abbr: "CB", label: "Coinbase" },
  kraken: { color: "#5741d9", abbr: "KR", label: "Kraken" },
  okx: { color: "#000000", abbr: "OX", label: "OKX" },
};

interface Props {
  price: PriceState | undefined;
  symbol: string;
}

export function SourcePanel({ price, symbol }: Props) {
  const sources = useSourcePrices(symbol);
  const statuses = useSourceStatus(symbol);
  const { data: feeds } = useQuery(feedHealthOptions());

  // Feed health per source (for latency only)
  const lagMap = new Map<string, number>();
  if (feeds) {
    for (const f of feeds) {
      const s = f.source ?? "";
      if (s && f.median_lag_ms) {
        lagMap.set(s, f.median_lag_ms);
      }
    }
  }

  const medianPrice = price ? parseFloat(price.price) : 0;

  return (
    <div id="sources" className={styles.wrap}>
      {/* Summary bar */}
      {price && (
        <div className={styles.summary}>
          <div className={styles.summaryItem}>
            <span className={styles.summaryLabel}>Basis</span>
            <span className={styles.summaryValue}>{formatBasis(price.basis)}</span>
          </div>
          <span className={styles.summaryDot} />
          <div className={styles.summaryItem}>
            <span className={styles.summaryLabel}>Quality</span>
            <span className={styles.summaryValue}>{price.qualityScore.toFixed(2)}</span>
          </div>
          <span className={styles.summaryDot} />
          <div className={styles.summaryItem}>
            <span className={styles.summaryLabel}>Sources</span>
            <span className={styles.summaryValue}>{price.sourceCount} active</span>
          </div>
        </div>
      )}

      {/* Source rows */}
      <div className={styles.table}>
        <div className={styles.tableHeader}>
          <span className={styles.colName}>Source</span>
          <span className={styles.colPrice}>Price</span>
          <span className={styles.colDelta}>Delta</span>
          <span className={styles.colLag}>Latency</span>
          <span className={styles.colStatus}>Status</span>
        </div>
        {EXCHANGES.map((name) => {
          const meta = EXCHANGE_META[name];
          const sp = sources[name];
          const p = sp ? parseFloat(sp.price) : null;
          const delta = p !== null && medianPrice ? p - medianPrice : null;
          const status = statuses[name];
          const connected = status ? status.connState === "connected" : false;
          const stale = status?.stale ?? false;

          return (
            <SourceRow
              key={name}
              meta={meta}
              price={p}
              delta={delta}
              lag={lagMap.get(name)}
              connected={connected}
              stale={stale}
            />
          );
        })}
      </div>
    </div>
  );
}

interface SourceRowProps {
  meta: { color: string; abbr: string; label: string };
  price: number | null;
  delta: number | null;
  lag: number | undefined;
  connected: boolean;
  stale: boolean;
}

function SourceRow({ meta, price, delta, lag, connected, stale }: SourceRowProps) {
  const prevPrice = useRef<number | null>(null);
  const [flash, setFlash] = useState<"up" | "down" | null>(null);

  useEffect(() => {
    if (prevPrice.current !== null && price !== null && prevPrice.current !== price) {
      setFlash(price > prevPrice.current ? "up" : "down");
      const t = setTimeout(() => setFlash(null), 500);
      return () => clearTimeout(t);
    }
    prevPrice.current = price;
  }, [price]);

  const statusColor = stale ? "var(--yellow)" : connected ? "var(--green)" : "var(--text-ghost)";

  return (
    <div className={styles.row}>
      <div className={styles.colName}>
        <span className={styles.icon} style={{ backgroundColor: meta.color }}>
          {meta.abbr}
        </span>
        <span className={styles.name}>{meta.label}</span>
      </div>
      <span className={`${styles.colPrice} ${flash === "up" ? styles.flashUp : ""} ${flash === "down" ? styles.flashDown : ""}`}>
        {price !== null
          ? price.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 })
          : "\u2014"}
      </span>
      <span className={`${styles.colDelta} ${delta !== null && delta > 0 ? styles.deltaUp : ""} ${delta !== null && delta < 0 ? styles.deltaDown : ""}`}>
        {delta !== null
          ? `${delta >= 0 ? "+" : ""}${delta.toFixed(2)}`
          : "\u2014"}
      </span>
      <span className={styles.colLag}>
        {lag !== undefined ? `${lag}ms` : "\u2014"}
      </span>
      <span className={styles.colStatus}>
        <span className={styles.statusDot} style={{ backgroundColor: statusColor }} />
      </span>
    </div>
  );
}

function formatBasis(basis: string) {
  return basis.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
}
