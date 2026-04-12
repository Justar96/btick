import { useEffect, useRef, useState } from "react";
import type { PriceState } from "@/ws/types";
import styles from "./PriceDisplay.module.css";

interface Props {
  price: PriceState;
}

export function PriceDisplay({ price }: Props) {
  const prevRef = useRef(price.price);
  const [flash, setFlash] = useState<"up" | "down" | null>(null);

  useEffect(() => {
    const prev = parseFloat(prevRef.current);
    const curr = parseFloat(price.price);
    if (prev && curr && prev !== curr) {
      setFlash(curr > prev ? "up" : "down");
      const t = setTimeout(() => setFlash(null), 600);
      return () => clearTimeout(t);
    }
    prevRef.current = price.price;
  }, [price.price]);

  const num = parseFloat(price.price);
  const formatted = num.toLocaleString("en-US", {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  });

  const ago = price.ts ? getAgo(price.ts) : "";

  return (
    <div id="price" className={styles.wrap}>
      <div className={styles.row}>
        <span className={`${styles.price} ${flash ? styles[flash] : ""}`}>
          {formatted}
        </span>
        <span className={styles.currency}>USD</span>
      </div>
      <div className={styles.meta}>
        <span className={styles.ago}>{ago}</span>
        <span className={styles.sep}>|</span>
        <span className={styles.basis}>{formatBasis(price.basis)}</span>
        {price.isStale && (
          <>
            <span className={styles.sep}>|</span>
            <span className={styles.stale}>Stale</span>
          </>
        )}
        {price.isDegraded && !price.isStale && (
          <>
            <span className={styles.sep}>|</span>
            <span className={styles.degraded}>Degraded</span>
          </>
        )}
      </div>
    </div>
  );
}

function formatBasis(basis: string) {
  return basis
    .replace(/_/g, " ")
    .replace(/\b\w/g, (c) => c.toUpperCase());
}

function getAgo(ts: string) {
  const ms = Date.now() - new Date(ts).getTime();
  if (ms < 1000) return "just now";
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s ago`;
  return `${Math.floor(ms / 60000)}m ago`;
}
