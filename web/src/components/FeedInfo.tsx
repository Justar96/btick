import type { SymbolMetadata } from "@/api/queries";
import styles from "./FeedInfo.module.css";

interface Props {
  metadata?: SymbolMetadata;
}

export function FeedInfo({ metadata }: Props) {
  return (
    <div id="feed-info" className={styles.wrap}>
      <h3 className={styles.heading}>Feed Metadata</h3>
      <div className={styles.rows}>
        <div className={styles.row}>
          <span className={styles.label}>Base Asset</span>
          <span className={styles.value}>{metadata?.base_asset ?? "--"}</span>
        </div>
        <div className={styles.row}>
          <span className={styles.label}>Quote Asset</span>
          <span className={styles.value}>{metadata?.quote_asset ?? "--"}</span>
        </div>
        <div className={styles.row}>
          <span className={styles.label}>Product Type</span>
          <span className={styles.value}>{formatLabel(metadata?.product_type)}</span>
        </div>
        <div className={styles.row}>
          <span className={styles.label}>Product Sub-type</span>
          <span className={styles.value}>{formatLabel(metadata?.product_sub_type)}</span>
        </div>
        <div className={styles.row}>
          <span className={styles.label}>Product Name</span>
          <span className={styles.value}>{metadata?.product_name ?? "--"}</span>
        </div>
        <div className={styles.row}>
          <span className={styles.label}>Market Hours</span>
          <span className={styles.value}>{metadata?.market_hours ?? "--"}</span>
        </div>
        <div className={styles.row}>
          <span className={styles.label}>Feed ID</span>
          <span className={styles.value}>{metadata?.feed_id ?? "--"}</span>
        </div>
      </div>
    </div>
  );
}

function formatLabel(value: string | undefined) {
  if (!value) return "--";
  return value.replace(/_/g, " ").replace(/\b\w/g, (c) => c.toUpperCase());
}
