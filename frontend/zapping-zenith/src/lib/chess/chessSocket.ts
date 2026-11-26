// frontend/src/lib/chess/chessSocket.ts
// One game's WebSocket. Push-only: moves go over HTTP. Reconnects with backoff;
// the server sends the full state first on every (re)connect, so nothing is missed.
import type { ChessState } from "../api/chess";

export interface ChessEvent {
  type: string;
  state?: ChessState;
  userId?: string;
  color?: string;
  result?: string;
  resultMethod?: string;
}

export interface ChessSocket { close(): void }

function wsUrl(groupId: string, gameId: string) {
  const api = ((import.meta.env.PUBLIC_API_URL as string | undefined) ?? "http://localhost:8080/api/v1").replace(/\/+$/, "");
  return `${api.replace(/^http/, "ws")}/groups/${encodeURIComponent(groupId)}/chess/games/${encodeURIComponent(gameId)}/ws`;
}

export function openChessSocket(
  groupId: string,
  gameId: string,
  onEvent: (e: ChessEvent) => void,
  onConnection?: (open: boolean) => void,
): ChessSocket {
  let ws: WebSocket | null = null;
  let stopped = false;
  let attempt = 0;
  let everOpened = false;
  let timer: number | undefined;

  const connect = () => {
    if (stopped) return;
    ws = new WebSocket(wsUrl(groupId, gameId));
    ws.onopen = () => { attempt = 0; everOpened = true; onConnection?.(true); };
    ws.onmessage = (m) => {
      try { onEvent(JSON.parse(String(m.data)) as ChessEvent); } catch { /* ignore junk */ }
    };
    ws.onclose = () => {
      ws = null;
      onConnection?.(false);
      if (stopped) return;
      // A handshake that never succeeds (refused, e.g. not a player) will not fix itself: stop hammering the server.
      if (!everOpened && attempt >= 5) { stopped = true; return; }
      const base = Math.min(15_000, 500 * 2 ** attempt++);
      timer = window.setTimeout(connect, base / 2 + Math.random() * (base / 2));
    };
    ws.onerror = () => { /* onclose follows */ };
  };
  connect();

  return {
    close() {
      stopped = true;
      window.clearTimeout(timer);
      ws?.close(1000, "leaving");
    },
  };
}