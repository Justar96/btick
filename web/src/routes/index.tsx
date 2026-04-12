import { lazy, Suspense, useEffect, useRef, useCallback } from "react";
import { Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { symbolsOptions } from "@/api/queries";
import styles from "./index.module.css";

const BitcoinCoin3D = lazy(() =>
  import("@/components/BitcoinCoin3D").then((m) => ({
    default: m.BitcoinCoin3D,
  })),
);

function symbolToSlug(symbol: string) {
  return symbol.toLowerCase().replace(/\//g, "-");
}

/** Clamp v to [0, 1]. */
function clamp01(v: number) {
  return Math.min(1, Math.max(0, v));
}

export function HomePage() {
  const { data: symbols } = useQuery(symbolsOptions());
  const firstSlug = symbols?.[0] ? symbolToSlug(symbols[0]) : "btc-usd";

  const heroRef = useRef<HTMLDivElement>(null);
  const coinRef = useRef<HTMLDivElement>(null);
  const logoRef = useRef<HTMLSpanElement>(null);
  const belowRef = useRef<HTMLDivElement>(null);
  // How far right coin needs to move to center above "btick" text
  const flipXRef = useRef(0);
  // Speed multiplier for 3D coin spin — written by scroll handler, read by useFrame
  const spinSpeedRef = useRef(1);

  const measure = useCallback(() => {
    const coin = coinRef.current;
    const logo = logoRef.current;
    if (!coin || !logo) return;
    const cr = coin.getBoundingClientRect();
    const lr = logo.getBoundingClientRect();
    flipXRef.current = lr.left + lr.width / 2 - (cr.left + cr.width / 2);
  }, []);

  const onScroll = useCallback(() => {
    const hero = heroRef.current;
    const coin = coinRef.current;
    const logo = logoRef.current;
    const below = belowRef.current;
    if (!hero || !coin || !logo || !below) return;

    const scrolled = -hero.getBoundingClientRect().top;
    const vh = window.innerHeight;
    const t = clamp01(scrolled / vh);

    // Flip arc progress — coin moves to above "btick" by 50% scroll
    const flipT = clamp01(t / 0.5);
    const eased = flipT * flipT * (3 - 2 * flipT); // smoothstep

    // Coin slides straight right to cover "btick" text
    const coinX = flipXRef.current * 1.6 * eased;
    const scale = 1 + eased * 0.15;
    coin.style.transform = `translate(${coinX}px, 0px) scale(${scale})`;

    // Spin speed ramps from 1x to 6x over the hero fade range
    spinSpeedRef.current = 1 + clamp01(t / 0.7) ** 2.5 * 5;

    // Text fades out later — coin overlaps text before it disappears
    const textAlpha = 1 - clamp01(t / 0.7);
    logo.style.opacity = String(textAlpha);
    below.style.opacity = String(textAlpha);
    below.style.pointerEvents = textAlpha < 0.1 ? "none" : "";
  }, []);

  useEffect(() => {
    measure();
    window.addEventListener("scroll", onScroll, { passive: true });
    window.addEventListener("resize", measure);
    onScroll();
    return () => {
      window.removeEventListener("scroll", onScroll);
      window.removeEventListener("resize", measure);
    };
  }, [onScroll, measure]);

  return (
    <>
      <div ref={heroRef} className={styles.hero}>
        <div className={styles.logoRow}>
          <div ref={coinRef} className={styles.coin}>
            <Suspense fallback={null}>
              <BitcoinCoin3D size={90} className={styles.coinCanvas} speedRef={spinSpeedRef} />
            </Suspense>
          </div>
          <span ref={logoRef} className={styles.logo}>btick</span>
        </div>
        <div ref={belowRef} className={styles.below}>
          <span className={styles.tagline}>
            Real-time price feeds, sub-second delivery
          </span>
          <div className={styles.actions}>
            <Link
              to="/$symbol"
              params={{ symbol: firstSlug }}
              className={`${styles.cta} ${styles.ctaLive}`}
            >
              View live feed
            </Link>
            <Link to="/api" hash="latest" className={styles.cta}>
              Read the docs
            </Link>
          </div>
        </div>
      </div>

      <section className={styles.features}>
        <span className={styles.featuresLabel}>Why btick</span>
        <div className={styles.featureRow}>
          <div className={styles.featureCard}>
            <div className={styles.featureIllustration}>
              <svg viewBox="0 0 200 100" className={styles.featureSvg}>
                <path d="M10 20 Q50 28, 80 38 Q110 48, 140 44 Q160 43, 185 44" fill="none" stroke="#f0b90b" strokeWidth="1.2" opacity="0.55" />
                <path d="M10 58 Q40 52, 70 46 Q100 41, 140 43 Q165 44, 185 44" fill="none" stroke="#0052ff" strokeWidth="1.2" opacity="0.55" />
                <path d="M10 72 Q45 62, 80 50 Q110 43, 140 44 Q160 44, 185 44" fill="none" stroke="#5741d9" strokeWidth="1.2" opacity="0.55" />
                <path d="M10 38 Q50 36, 80 40 Q110 43, 140 44 Q160 44, 185 44" fill="none" stroke="#1a1a1a" strokeWidth="1.2" opacity="0.45" />
                <path d="M140 44 L190 44" fill="none" stroke="#1a1a1a" strokeWidth="2.5" />
                <circle cx="190" cy="44" r="3" fill="#1a1a1a" />
                <line x1="138" y1="16" x2="138" y2="78" stroke="#ebebeb" strokeWidth="1" strokeDasharray="3,3" />
                <text x="138" y="90" fontSize="7" fill="#ccc" textAnchor="middle" fontFamily="system-ui">median</text>
              </svg>
            </div>
            <h3 className={styles.featureTitle}>Multi-venue median</h3>
            <p className={styles.featureDesc}>4 exchanges, 1 canonical price</p>
          </div>

          <div className={styles.featureCard}>
            <div className={styles.featureIllustration}>
              <svg viewBox="0 0 200 100" className={styles.featureSvg}>
                <line x1="20" y1="50" x2="180" y2="50" stroke="#ebebeb" strokeWidth="1" />
                <line x1="40" y1="44" x2="40" y2="56" stroke="#ccc" strokeWidth="1" />
                <line x1="70" y1="44" x2="70" y2="56" stroke="#ccc" strokeWidth="1" />
                <line x1="100" y1="44" x2="100" y2="56" stroke="#ccc" strokeWidth="1" />
                <line x1="130" y1="44" x2="130" y2="56" stroke="#ccc" strokeWidth="1" />
                <line x1="160" y1="44" x2="160" y2="56" stroke="#1a1a1a" strokeWidth="1.5" />
                <circle cx="160" cy="50" r="16" fill="none" stroke="#1a1a1a" strokeWidth="0.5" opacity="0.12" />
                <circle cx="160" cy="50" r="10" fill="none" stroke="#1a1a1a" strokeWidth="0.8" opacity="0.25" />
                <circle cx="160" cy="50" r="4" fill="#1a1a1a" />
                <text x="40" y="70" fontSize="7" fill="#ccc" textAnchor="middle" fontFamily="monospace">t-4</text>
                <text x="70" y="70" fontSize="7" fill="#ccc" textAnchor="middle" fontFamily="monospace">t-3</text>
                <text x="100" y="70" fontSize="7" fill="#ccc" textAnchor="middle" fontFamily="monospace">t-2</text>
                <text x="130" y="70" fontSize="7" fill="#ccc" textAnchor="middle" fontFamily="monospace">t-1</text>
                <text x="160" y="70" fontSize="7" fill="#1a1a1a" textAnchor="middle" fontFamily="monospace" fontWeight="600">now</text>
                <text x="55" y="38" fontSize="6" fill="#999" textAnchor="middle" fontFamily="system-ui">1s</text>
                <line x1="42" y1="40" x2="68" y2="40" stroke="#999" strokeWidth="0.5" />
              </svg>
            </div>
            <h3 className={styles.featureTitle}>Sub-second delivery</h3>
            <p className={styles.featureDesc}>1s snapshots, live WebSocket stream</p>
          </div>
        </div>
      </section>
    </>
  );
}
