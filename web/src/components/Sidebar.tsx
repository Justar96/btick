import { Link, useParams, useMatchRoute, useLocation } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { symbolsOptions } from "@/api/queries";
import { CoinIcon } from "./CoinIcon";
import styles from "./Sidebar.module.css";

const API_SECTIONS = [
  { id: "latest", label: "Latest Price" },
  { id: "settlement", label: "Settlement" },
  { id: "snapshots", label: "Snapshots" },
  { id: "websocket", label: "WebSocket" },
  { id: "health", label: "Health" },
  { id: "endpoints", label: "All Endpoints" },
];

function symbolToSlug(symbol: string) {
  return symbol.toLowerCase().replace(/\//g, "-");
}

export function Sidebar() {
  const { symbol: activeSlug } = useParams({ strict: false }) as { symbol?: string };
  const matchRoute = useMatchRoute();
  const location = useLocation();
  const isApiPage = !!matchRoute({ to: "/api" });
  const { data: symbols } = useQuery(symbolsOptions());

  const activeHash = location.hash?.replace("#", "") || "latest";

  return (
    <aside className={styles.sidebar}>
      <div className={styles.logo}>
        <Link to="/">btick</Link>
      </div>

      <nav className={styles.nav}>
        {(symbols ?? []).map((sym) => {
          const slug = symbolToSlug(sym);
          const isActive = slug === activeSlug && !isApiPage;
          return (
            <Link
              key={sym}
              to="/$symbol"
              params={{ symbol: slug }}
              className={`${styles.navLink} ${isActive ? styles.navLinkActive : ""}`}
            >
              <CoinIcon symbol={sym} size={14} className={styles.coinIcon} />
              <span>{sym.replace("/", " / ")}</span>
            </Link>
          );
        })}

        <div className={styles.divider} />

        <Link
          to="/api"
          hash="latest"
          className={`${styles.navLink} ${isApiPage ? styles.navLinkActive : ""}`}
        >
          <span className={styles.navIcon}>API</span>
          <span>Docs</span>
        </Link>

        {isApiPage && (
          <div className={styles.sectionLinks}>
            {API_SECTIONS.map((s) => (
              <Link
                key={s.id}
                to="/api"
                hash={s.id}
                className={`${styles.sectionLink} ${activeHash === s.id ? styles.sectionLinkActive : ""}`}
              >
                {s.label}
              </Link>
            ))}
          </div>
        )}
      </nav>

      <div className={styles.spacer} />
    </aside>
  );
}
