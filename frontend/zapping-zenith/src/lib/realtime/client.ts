import { authApi } from "../api/auth";
import { ApiError } from "../api/client";
import type { Envelope, HelloPayload, RealtimeStatus } from "./types";

/**
 * Browser side of Rovingr's WebSocket connection.
 *
 * One RealtimeClient per tab. It:
 *  - connects with the session cookie (no token in the URL),
 *  - reconnects with exponential backoff + jitter,
 *  - sends an app-level ping (browsers can't send protocol pings) to notice dead links,
 *  - tells you the status and lets you subscribe to message types.
 *
 * It knows nothing about rooms, chat or calls. Later stages subscribe with on("room.state", ...).
 */

export function realtimeUrl(): string {
  const override = import.meta.env.PUBLIC_WS_URL as string | undefined;
  if (override) return override;
  const api = (import.meta.env.PUBLIC_API_URL ?? "http://localhost:8080/api/v1").replace(/\/+$/, "");
  return api.replace(/^http/, "ws") + "/realtime/ws";
}

type Handler = (env: Envelope) => void;
type StatusHandler = (s: RealtimeStatus, detail?: string) => void;

const PING_EVERY_MS = 20_000;
const PONG_WAIT_MS = 10_000;
const BACKOFF_MIN = 500;
const BACKOFF_MAX = 15_000;

export class RealtimeClient {
  status: RealtimeStatus = "idle";
  hello: HelloPayload | null = null;

  private ws: WebSocket | null = null;
  private handlers = new Map<string, Set<Handler>>();
  private statusHandlers = new Set<StatusHandler>();
  private attempt = 0;
  private everOpened = false;
  private stopped = false;
  private retryTimer: number | undefined;
  private pingTimer: number | undefined;
  private pongTimer: number | undefined;
  private seq = 0;

  constructor(private url: string = realtimeUrl()) {
    // Coming back online or to the tab: don't wait out the backoff.
    window.addEventListener("online", () => this.nudge());
    document.addEventListener("visibilitychange", () => { if (!document.hidden) this.nudge(); });
  }

  start() {
    this.stopped = false;
    if (this.ws || this.status === "connecting") return;
    this.open();
  }

  stop() {
    this.stopped = true;
    window.clearTimeout(this.retryTimer);
    this.clearHeartbeat();
    this.ws?.close(1000, "client closing");
    this.ws = null;
    this.setStatus("closed");
  }

  on(type: string, fn: Handler): () => void {
    if (!this.handlers.has(type)) this.handlers.set(type, new Set());
    this.handlers.get(type)!.add(fn);
    return () => this.handlers.get(type)?.delete(fn);
  }

  onStatus(fn: StatusHandler): () => void {
    this.statusHandlers.add(fn);
    fn(this.status);
    return () => this.statusHandlers.delete(fn);
  }

  /** Returns the message id, or null when the socket isn't open (nothing is queued). */
  send(type: string, payload?: unknown): string | null {
    if (this.ws?.readyState !== WebSocket.OPEN) return null;
    const id = `c-${++this.seq}`;
    this.ws.send(JSON.stringify({ type, id, payload }));
    return id;
  }

  // ---- internals ----

  private setStatus(s: RealtimeStatus, detail?: string) {
    this.status = s;
    this.statusHandlers.forEach((fn) => fn(s, detail));
  }

  private open() {
    this.setStatus(this.attempt === 0 ? "connecting" : "reconnecting");
    let ws: WebSocket;
    try {
      ws = new WebSocket(this.url);
    } catch (e) {
      this.scheduleRetry();
      return;
    }
    this.ws = ws;

    ws.onopen = () => {
      this.attempt = 0;
      this.everOpened = true;
      this.setStatus("open");
      this.startHeartbeat();
    };

    ws.onmessage = (ev) => {
      let env: Envelope;
      try { env = JSON.parse(String(ev.data)); } catch { return; }
      if (!env || typeof env.type !== "string") return;
      if (env.type === "hello") this.hello = env.payload as HelloPayload;
      if (env.type === "pong") window.clearTimeout(this.pongTimer);
      this.handlers.get(env.type)?.forEach((fn) => fn(env));
      this.handlers.get("*")?.forEach((fn) => fn(env));
    };

    ws.onclose = (ev) => {
      this.clearHeartbeat();
      this.ws = null;
      this.hello = null;
      if (this.stopped) return;
      // 1008 = server ended our session (logout/expiry) or rate-limited us.
      // A handshake that never opened is usually a 401: browsers hide the status, so ask the API.
      if (ev.code === 1008 || !this.everOpened) void this.checkSession(ev.code);
      else this.scheduleRetry();
    };

    ws.onerror = () => { /* onclose follows with the details */ };
  }

  private async checkSession(code: number) {
    try {
      await authApi.me();
      this.scheduleRetry(); // still logged in: a normal network/server problem
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        this.stopped = true;
        this.setStatus("unauthorized", "Your session has ended. Please sign in again.");
      } else {
        this.scheduleRetry(); // API unreachable too; keep trying
      }
    }
  }

  private scheduleRetry() {
    if (this.stopped) return;
    const base = Math.min(BACKOFF_MAX, BACKOFF_MIN * 2 ** this.attempt);
    const delay = base / 2 + Math.random() * (base / 2); // jitter avoids thundering herds after a restart
    this.attempt++;
    this.setStatus("reconnecting", `retrying in ${Math.round(delay / 100) / 10}s`);
    window.clearTimeout(this.retryTimer);
    this.retryTimer = window.setTimeout(() => this.open(), delay);
  }

  private nudge() {
    if (this.stopped || this.status === "open" || this.status === "unauthorized") return;
    if (this.status === "reconnecting") {
      window.clearTimeout(this.retryTimer);
      this.open();
    }
  }

  private startHeartbeat() {
    this.clearHeartbeat();
    this.pingTimer = window.setInterval(() => {
      if (this.ws?.readyState !== WebSocket.OPEN) return;
      this.send("ping");
      window.clearTimeout(this.pongTimer);
      // No pong: the link is dead even though the browser hasn't noticed. Drop it and reconnect.
      this.pongTimer = window.setTimeout(() => this.ws?.close(4000, "pong timeout"), PONG_WAIT_MS);
    }, PING_EVERY_MS);
  }

  private clearHeartbeat() {
    window.clearInterval(this.pingTimer);
    window.clearTimeout(this.pongTimer);
  }
}
