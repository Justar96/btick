import { useRef, useEffect, useCallback } from "react";
import type { WSMessage, WSClientAction } from "./types";

const RECONNECT_BASE = 1000;
const RECONNECT_MAX = 30000;

interface UseWebSocketOptions {
  url: string;
  onMessage: (msg: WSMessage) => void;
  onStatusChange?: (connected: boolean) => void;
  onReconnect?: () => void;
}

export function useWebSocket({ url, onMessage, onStatusChange, onReconnect }: UseWebSocketOptions) {
  const wsRef = useRef<WebSocket | null>(null);
  const attemptRef = useRef(0);
  const timerRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const onMessageRef = useRef(onMessage);
  const onStatusRef = useRef(onStatusChange);
  const onReconnectRef = useRef(onReconnect);
  const wasConnectedRef = useRef(false);
  const mountedRef = useRef(true);
  const urlRef = useRef(url);

  onMessageRef.current = onMessage;
  onStatusRef.current = onStatusChange;
  onReconnectRef.current = onReconnect;
  urlRef.current = url;

  const connect = useCallback(() => {
    if (!mountedRef.current) return;

    // Close any existing socket cleanly before opening a new one
    const prev = wsRef.current;
    if (prev) {
      prev.onopen = null;
      prev.onmessage = null;
      prev.onclose = null;
      prev.onerror = null;
      if (prev.readyState === WebSocket.OPEN || prev.readyState === WebSocket.CONNECTING) {
        prev.close();
      }
      wsRef.current = null;
    }

    const ws = new WebSocket(urlRef.current);
    wsRef.current = ws;

    ws.onopen = () => {
      if (!mountedRef.current) { ws.close(); return; }
      const isReconnect = wasConnectedRef.current;
      attemptRef.current = 0;
      wasConnectedRef.current = true;
      onStatusRef.current?.(true);

      const sub: WSClientAction = {
        action: "subscribe",
        types: ["latest_price", "snapshot_1s", "source_price", "source_status"],
      };
      ws.send(JSON.stringify(sub));

      if (isReconnect) {
        onReconnectRef.current?.();
      }
    };

    ws.onmessage = (e) => {
      try {
        const msg: WSMessage = JSON.parse(e.data);
        onMessageRef.current(msg);
      } catch {
        // ignore malformed messages
      }
    };

    ws.onclose = () => {
      onStatusRef.current?.(false);
      if (!mountedRef.current) return;
      const delay = Math.min(RECONNECT_BASE * 2 ** attemptRef.current, RECONNECT_MAX);
      attemptRef.current++;
      timerRef.current = setTimeout(connect, delay);
    };

    ws.onerror = () => {
      ws.close();
    };
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    connect();
    return () => {
      mountedRef.current = false;
      clearTimeout(timerRef.current);
      const ws = wsRef.current;
      if (ws) {
        ws.onopen = null;
        ws.onmessage = null;
        ws.onclose = null;
        ws.onerror = null;
        ws.close();
        wsRef.current = null;
      }
    };
  }, [connect]);
}
