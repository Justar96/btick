import { useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { symbolMetadataOptions } from "@/api/queries";
import { FeedInfo } from "@/components/FeedInfo";
import { CoinIcon } from "@/components/CoinIcon";
import { usePrice } from "@/ws/context";
import { PriceDisplay } from "@/components/PriceDisplay";
import { PriceChart } from "@/components/PriceChart";
import { SourcePanel } from "@/components/SourcePanel";
import styles from "./coin.$symbol.module.css";

function slugToSymbol(slug: string) {
  return slug.toUpperCase().replace("-", "/");
}

export function CoinPage() {
  const { symbol: slug } = useParams({ strict: false }) as { symbol: string };
  const symbol = slugToSymbol(slug);
  const price = usePrice(symbol);
  const { data: metadata } = useQuery(symbolMetadataOptions(symbol));

  const base = slug.split("-")[0];

  const today = new Date().toLocaleDateString("en-US", {
    day: "numeric",
    month: "long",
    year: "numeric",
  });

  return (
    <div className={styles.page}>
      {/* Title block */}
      <div className={styles.header}>
        <div className={styles.titleRow}>
          <CoinIcon symbol={slug} size={26} />
          <h1 className={styles.title}>{symbol.replace("/", " / ")}</h1>
        </div>
        <div className={styles.date}>{today}</div>
        <p className={styles.desc}>
          Real-time {base.toUpperCase()} price aggregated from multiple exchange sources via multi-venue median.
          Sub-second precision for prediction market settlement.
        </p>
      </div>

      {/* Price */}
      {price ? (
        <PriceDisplay price={price} />
      ) : (
        <div className={styles.loading}>Connecting to price feed...</div>
      )}

      {/* Chart */}
      <PriceChart symbol={symbol} />

      {/* Sources + Feed info + Health — unified */}
      <SourcePanel price={price} symbol={symbol} />

      <FeedInfo metadata={metadata} />
    </div>
  );
}
