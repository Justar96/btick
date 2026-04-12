import { Outlet, useMatchRoute } from "@tanstack/react-router";
import { WebSocketProvider } from "@/ws/context";
import { useWSConnected } from "@/ws/context";
import { Sidebar } from "@/components/Sidebar";
import styles from "./__root.module.css";

function Footer() {
  const connected = useWSConnected();
  return (
    <footer className={styles.footer}>
      <div className={styles.footerLeft}>
        <span className={`dot ${connected ? "dot--ok" : "dot--error"}`} />
        <span>{connected ? "Live" : "Reconnecting..."}</span>
      </div>
      <div className={styles.footerRight}>
        <span>btick price oracle</span>
        <span className={styles.footerDot}>&middot;</span>
        <a
          href="https://github.com/justar96/btick"
          target="_blank"
          rel="noopener noreferrer"
          className={styles.footerLink}
        >
          GitHub
        </a>
        <span className={styles.footerDot}>&middot;</span>
        <a href="/api" className={styles.footerLink}>
          API Docs
        </a>
      </div>
    </footer>
  );
}

export function RootLayout() {
  const matchRoute = useMatchRoute();
  const isHome = !!matchRoute({ to: "/" });

  return (
    <WebSocketProvider>
      <div className={isHome ? styles.layoutFull : styles.layout}>
        {!isHome && <Sidebar />}
        <div className={styles.mainWrap}>
          <main className={styles.main}>
            <Outlet />
          </main>
          <Footer />
        </div>
      </div>
    </WebSocketProvider>
  );
}
