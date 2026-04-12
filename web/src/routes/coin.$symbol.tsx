import { useParams } from "@tanstack/react-router";
import { usePrice } from "@/ws/context";
import { PriceDisplay } from "@/components/PriceDisplay";
import { PriceChart } from "@/components/PriceChart";
import { SourcePanel } from "@/components/SourcePanel";
import { IntegrateSection } from "@/components/IntegrateSection";
import styles from "./coin.$symbol.module.css";

const COIN_ICONS: Record<string, { color: string; letter: string }> = {
  btc: { color: "#f7931a", letter: "B" },
  eth: { color: "#627eea", letter: "E" },
  sol: { color: "#9945ff", letter: "S" },
  avax: { color: "#e84142", letter: "A" },
  doge: { color: "#c2a633", letter: "D" },
};

function slugToSymbol(slug: string) {
  return slug.toUpperCase().replace("-", "/");
}

export function CoinPage() {
  const { symbol: slug } = useParams({ strict: false }) as { symbol: string };
  const symbol = slugToSymbol(slug);
  const price = usePrice(symbol);

  const base = slug.split("-")[0];
  const icon = COIN_ICONS[base] ?? { color: "#999", letter: base[0]?.toUpperCase() ?? "?" };

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
          <svg width="26" height="26" viewBox="0 0 32 32">
            <circle cx="16" cy="16" r="16" fill={icon.color} />
            <text x="16" y="21" textAnchor="middle" fontSize="14" fontWeight="700" fill="white" fontFamily="Arial">
              {icon.letter}
            </text>
          </svg>
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

      {/* API integration docs */}
      <IntegrateSection />
    </div>
  );
}
