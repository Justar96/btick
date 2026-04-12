import { useEffect, useRef, useState } from "react";
import { useSourcePrices } from "@/ws/context";
import styles from "./SourceList.module.css";

const EXCHANGE_META: Record<string, { color: string; abbr: string; label: string }> = {
  binance: { color: "#f0b90b", abbr: "BN", label: "Binance" },
  coinbase: { color: "#0052ff", abbr: "CB", label: "Coinbase" },
  kraken: { color: "#5741d9", abbr: "KR", label: "Kraken" },
  okx: { color: "#000000", abbr: "OX", label: "OKX" },
};

interface SourceEntry {
  source: string;
  price: number;
  ts: string;
  visible: boolean;
  stale: boolean;
}

interface Props {
  medianPrice: number;
  symbol: string;
}

export function SourceList({ medianPrice, symbol }: Props) {
  const sourcePrices = useSourcePrices(symbol);
  const [sources, setSources] = useState<Map<string, SourceEntry>>(new Map());
  const prevPrices = useRef<Map<string, number>>(new Map());
  const [flashes, setFlashes] = useState<Map<string, "up" | "down">>(new Map());

  useEffect(() => {
    setSources((prev) => {
      const next = new Map(prev);

      for (const [source, sp] of Object.entries(sourcePrices)) {
        const price = parseFloat(sp.price);
        const prevPrice = prevPrices.current.get(source);

        if (prevPrice !== undefined && prevPrice !== price) {
          setFlashes((f) => {
            const n = new Map(f);
            n.set(source, price > prevPrice ? "up" : "down");
            return n;
          });
          setTimeout(() => {
            setFlashes((f) => {
              const n = new Map(f);
              n.delete(source);
              return n;
            });
          }, 500);
        }
        prevPrices.current.set(source, price);

        next.set(source, {
          source,
          price,
          ts: sp.ts,
          visible: true,
          stale: false,
        });
      }

      return next;
    });
  }, [sourcePrices]);

  const sorted = Array.from(sources.values()).sort((a, b) =>
    a.source.localeCompare(b.source),
  );

  return (
    <div id="sources" className={styles.wrap}>
      <h3 className={styles.heading}>Source Prices</h3>
      <div className={styles.list}>
        {sorted.map((entry) => {
          const meta = EXCHANGE_META[entry.source] ?? {
            color: "#999",
            abbr: entry.source.slice(0, 2).toUpperCase(),
            label: entry.source,
          };
          const delta = medianPrice ? entry.price - medianPrice : 0;
          const flash = flashes.get(entry.source);
          const ago = getAgo(entry.ts);

          return (
            <div
              key={entry.source}
              className={`${styles.row} ${entry.stale ? styles.rowStale : styles.rowVisible}`}
            >
              <div className={styles.nameCol}>
                <span
                  className={styles.icon}
                  style={{ backgroundColor: meta.color }}
                >
                  {meta.abbr}
                </span>
                <div className={styles.nameGroup}>
                  <span className={styles.name}>{meta.label}</span>
                  <span className={styles.ago}>{ago}</span>
                </div>
              </div>
              <div className={styles.priceCol}>
                <span
                  className={`${styles.price} ${flash === "up" ? styles.flashUp : ""} ${flash === "down" ? styles.flashDown : ""}`}
                >
                  {entry.price.toLocaleString("en-US", {
                    minimumFractionDigits: 2,
                    maximumFractionDigits: 2,
                  })}
                </span>
                <span
                  className={`${styles.delta} ${delta > 0 ? styles.deltaUp : ""} ${delta < 0 ? styles.deltaDown : ""}`}
                >
                  {delta >= 0 ? "+" : ""}
                  {delta.toFixed(2)}
                </span>
              </div>
            </div>
          );
        })}
        {sorted.length === 0 && (
          <div className={styles.empty}>Waiting for source data...</div>
        )}
      </div>
    </div>
  );
}

function getAgo(ts: string) {
  if (!ts) return "";
  const ms = Date.now() - new Date(ts).getTime();
  if (ms < 1000) return "just now";
  if (ms < 60000) return `${(ms / 1000).toFixed(0)}s ago`;
  return `${Math.floor(ms / 60000)}m ago`;
}
