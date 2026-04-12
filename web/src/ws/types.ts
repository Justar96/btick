export interface WSMessage {
  type: "welcome" | "latest_price" | "snapshot_1s" | "heartbeat" | "source_price" | "source_status";
  seq?: number;
  symbol?: string;
  ts?: string;
  price?: string;
  basis?: string;
  is_stale?: boolean;
  is_degraded?: boolean;
  quality_score?: string;
  source_count?: number;
  sources_used?: string[];
  message?: string;
  source?: string;
  conn_state?: string;
  stale?: boolean;
}

export interface WSClientAction {
  action: "subscribe" | "unsubscribe";
  types: string[];
}

export interface PriceState {
  symbol: string;
  price: string;
  ts: string;
  basis: string;
  isStale: boolean;
  isDegraded: boolean;
  qualityScore: number;
  sourceCount: number;
  sourcesUsed: string[];
}

export interface SourcePrice {
  source: string;
  price: string;
  ts: string;
}

export interface SourceStatus {
  source: string;
  connState: string;
  stale: boolean;
  ts: string;
}
