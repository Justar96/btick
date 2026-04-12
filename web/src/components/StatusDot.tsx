type Status = "ok" | "warn" | "error" | "muted";

export function StatusDot({ status }: { status: Status }) {
  return <span className={`dot dot--${status}`} />;
}
