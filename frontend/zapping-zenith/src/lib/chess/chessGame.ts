// frontend/src/lib/chess/chessGame.ts
// One game: board (chessground), clocks, moves, actions. The SERVER is the referee:
// we only draw what it says and propose moves; if it disagrees the board snaps back.
import { Chessground } from "chessground";
import type { Api } from "chessground/api";
import type { Key } from "chessground/types";
import "chessground/assets/chessground.base.css";
import "chessground/assets/chessground.brown.css";
import "chessground/assets/chessground.cburnett.css";
import "../../styles/chess.css";

import { chessApi, type ChessState, type Color } from "../api/chess";
import { describeError } from "../api/client";
import { openChessSocket, type ChessEvent, type ChessSocket } from "./chessSocket";

export interface GameHandle { destroy(): void }
export interface GameOptions {
  groupId: string;
  gameId: string;
  meUserId: string;
  onExit(): void;
  announce?(text: string): void;
}

const METHOD: Record<string, string> = {
  checkmate: "checkmate", resignation: "resignation", flag: "timeout", draw_agreement: "agreement",
  stalemate: "stalemate", threefold_repetition: "repetition", fivefold_repetition: "repetition",
  fifty_move: "the fifty-move rule", seventy_five_move: "the 75-move rule", insufficient_material: "insufficient material",
};

function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls = "", text?: string) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function fmtClock(ms: number): string {
  if (ms < 10_000) return `0:${(ms / 1000).toFixed(1).padStart(4, "0")}`;
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), sec = s % 60;
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${String(sec).padStart(2, "0")}` : `${m}:${String(sec).padStart(2, "0")}`;
}

export function openGame(container: HTMLElement, opts: GameOptions): GameHandle {
  const { groupId, gameId, meUserId } = opts;
  let state: ChessState | null = null;
  let cg: Api | null = null;
  let offset = 0;                 // server time minus local time
  let busy = false;
  let timeoutSent = false;
  let opponentOnline: boolean | null = null;
  let destroyed = false;
  let resizeObs: ResizeObserver | null = null;

  /* ---------- DOM ---------- */
  container.replaceChildren();
  const root = el("div", "ch-game");
  const boardCol = el("div", "ch-game__board");
  const topBar = el("div", "ch-player");
  const boardEl = el("div", "ch-board");
  const bottomBar = el("div", "ch-player");
  const promo = el("div", "ch-promo"); promo.hidden = true;
  boardCol.append(topBar, boardEl, bottomBar, promo);

  const side = el("div", "ch-game__side");
  const statusEl = el("p", "ch-status"); statusEl.setAttribute("role", "status");
  const errEl = el("p", "ch-error"); errEl.setAttribute("role", "alert"); errEl.hidden = true;
  const actions = el("div", "ch-actions");
  const movesEl = el("ol", "ch-moves"); movesEl.setAttribute("aria-label", "Moves");
  const back = el("button", "btn btn--ghost btn--sm", "← Back to lobby"); back.type = "button";
  back.addEventListener("click", () => opts.onExit());
  side.append(back, statusEl, errEl, actions, movesEl);
  root.append(boardCol, side);
  container.append(root);

  const topName = el("span", "ch-player__name"), topDot = el("span", "ch-dot"), topClock = el("span", "ch-clock");
  const botName = el("span", "ch-player__name"), botDot = el("span", "ch-dot"), botClock = el("span", "ch-clock");
  topDot.setAttribute("aria-hidden", "true"); botDot.setAttribute("aria-hidden", "true");
  topBar.append(topDot, topName, topClock);
  bottomBar.append(botDot, botName, botClock);

  /* ---------- helpers ---------- */
  const myColor = (): Color | null => {
    if (!state) return null;
    if (state.white?.userId === meUserId) return "w";
    if (state.black?.userId === meUserId) return "b";
    return null;
  };
  const cgColor = (c: Color) => (c === "w" ? "white" : "black");
  const showError = (msg: string) => { errEl.textContent = msg; errEl.hidden = !msg; };

  function clockMs(c: Color): number | null {
    if (!state) return null;
    const base = c === "w" ? state.clock.whiteRemainingMs : state.clock.blackRemainingMs;
    if (base == null) return null;
    if (state.status === "active" && state.turnColor === c && state.clock.startedAt) {
      return Math.max(0, base - (Date.now() + offset - Date.parse(state.clock.startedAt)));
    }
    return base;
  }

  function resultText(s: ChessState): string {
    const how = METHOD[s.resultMethod ?? ""] ?? "";
    if (s.result === "1/2-1/2") return `Draw by ${how}`;
    const me = myColor();
    const whiteWon = s.result === "1-0";
    if (!me) return `${whiteWon ? "White" : "Black"} won by ${how}`;
    return `${whiteWon === (me === "w") ? "You won" : "You lost"} by ${how}`;
  }

  /* ---------- board ---------- */
  function applyBoard(s: ChessState) {
    const me = myColor();
    const myTurn = !!me && s.status === "active" && s.turnColor === me;
    const dests = new Map<Key, Key[]>();
    if (myTurn && s.dests) for (const [from, to] of Object.entries(s.dests)) dests.set(from as Key, to as Key[]);
    const config = {
      fen: s.fen,
      turnColor: cgColor(s.turnColor) as "white" | "black",
      check: s.check ? (cgColor(s.check) as "white" | "black") : false,
      lastMove: (s.lastMove ?? undefined) as Key[] | undefined,
      viewOnly: !me || s.status !== "active",
      movable: { free: false, color: me && s.status === "active" ? cgColor(me) : undefined, dests, showDests: true, events: { after: onUserMove } },
      premovable: { enabled: false },
      draggable: { showGhost: true },
      highlight: { lastMove: true, check: true },
    } as const;
    if (!cg) {
      cg = Chessground(boardEl, { ...config, orientation: me ? cgColor(me) : "white", coordinates: true, animation: { enabled: true, duration: 180 } } as never);
      // The board lives in a tab that can be hidden/resized: redraw whenever its box changes.
      resizeObs = new ResizeObserver(() => cg?.redrawAll());
      resizeObs.observe(boardEl);
    } else {
      cg.set({ ...config, orientation: me ? cgColor(me) : "white" } as never);
    }
  }

  function onUserMove(orig: Key, dest: Key) {
    if (!state || busy) { if (state) applyBoard(state); return; }
    const piece = cg?.state.pieces.get(dest);
    const isPromotion = piece?.role === "pawn" && (dest[1] === "8" || dest[1] === "1");
    if (isPromotion) askPromotion(orig, dest);
    else void submit(orig, dest, "");
  }

  function askPromotion(orig: Key, dest: Key) {
    promo.replaceChildren();
    promo.append(el("p", "ch-promo__title", "Promote to"));
    const row = el("div", "ch-promo__row");
    const pieces: [string, string, string][] = [["q", "♕", "Queen"], ["r", "♖", "Rook"], ["b", "♗", "Bishop"], ["n", "♘", "Knight"]];
    for (const [code, glyph, name] of pieces) {
      const b = el("button", "btn ch-promo__btn"); b.type = "button";
      b.setAttribute("aria-label", name);
      b.append(el("span", "ch-promo__glyph", glyph), el("span", "", name));
      b.addEventListener("click", () => { promo.hidden = true; void submit(orig, dest, code); });
      row.append(b);
    }
    const cancel = el("button", "btn btn--ghost btn--sm", "Cancel"); cancel.type = "button";
    cancel.addEventListener("click", () => { promo.hidden = true; if (state) applyBoard(state); });
    promo.append(row, cancel);
    promo.hidden = false;
    (row.firstElementChild as HTMLElement | null)?.focus();
  }

  async function submit(orig: string, dest: string, promotion: string) {
    if (!state) return;
    busy = true; showError("");
    try {
      const res = await chessApi.move(groupId, gameId, { orig, dest, promotion, version: state.version });
      applyState(res.state);
    } catch (err) {
      showError(describeError(err));
      await refresh(); // snap back to what the server says
    } finally { busy = false; }
  }

  /* ---------- rendering ---------- */
  function applyState(s: ChessState) {
    if (destroyed || s.id !== gameId) return;
    if (state && s.version < state.version) return; // an older snapshot arrived late
    const wasActive = state?.status === "active";
    state = s;
    if (s.serverTime) offset = Date.parse(s.serverTime) - Date.now();
    applyBoard(s);
    render();
    if (wasActive && s.status === "completed") opts.announce?.(resultText(s));
    ensureSocket();
  }

  async function refresh() {
    try { applyState(await chessApi.get(groupId, gameId)); }
    catch (err) { showError(describeError(err)); }
  }

  function render() {
    const s = state;
    if (!s) return;
    const me = myColor();
    const bottom: Color = me ?? "w";
    const top: Color = bottom === "w" ? "b" : "w";
    const nameOf = (c: Color) => (c === "w" ? s.white : s.black)?.displayName ?? "Waiting for opponent…";

    topName.textContent = nameOf(top);
    botName.textContent = me ? `${nameOf(bottom)} (you)` : nameOf(bottom);
    const timed = s.clock.initialSeconds > 0;
    topClock.hidden = botClock.hidden = !timed;
    // Online dot: the opponent sits at the top; show it only once the socket has told us something.
    topDot.hidden = botDot.hidden = true;
    if (me && opponentOnline !== null) {
      topDot.hidden = false;
      topDot.dataset.online = String(opponentOnline);
      topDot.title = opponentOnline ? "Online" : "Offline";
    }
    tickClocks();

    // status
    if (s.status === "pending") statusEl.textContent = "Waiting for an opponent to join…";
    else if (s.status === "completed") statusEl.textContent = resultText(s);
    else if (!me) statusEl.textContent = `You are not a player in this game, so you can watch but not move. ${s.turnColor === "w" ? "White" : "Black"} to move.`;
    else if (s.drawOffer && s.drawOffer !== me) statusEl.textContent = "Your opponent offers a draw";
    else if (s.drawOffer === me) statusEl.textContent = "Draw offered. Waiting for a reply";
    else statusEl.textContent = s.turnColor === me ? "Your move" : "Opponent's move";
    statusEl.dataset.tone = s.status === "completed" ? "done" : s.status === "active" && s.turnColor === me ? "mine" : "";

    // actions
    actions.replaceChildren();
    const btn = (label: string, fn: () => Promise<unknown>, cls = "btn btn--ghost btn--sm", confirmText?: string) => {
      const b = el("button", cls, label); b.type = "button";
      b.addEventListener("click", async () => {
        if (confirmText && !window.confirm(confirmText)) return;
        b.disabled = true; showError("");
        try { const r = await fn(); if (r && typeof r === "object" && "id" in (r as object)) applyState(r as ChessState); }
        catch (err) { showError(describeError(err)); await refresh(); }
        finally { b.disabled = false; }
      });
      actions.append(b);
    };
    if (s.status === "pending" && me) { // only the creator is seated while pending
      btn("Cancel game", async () => { await chessApi.cancel(groupId, gameId); opts.onExit(); }, "btn btn--ghost btn--sm", "Cancel this game?");
    }
    if (s.status === "active" && me) {
      if (s.drawOffer && s.drawOffer !== me) {
        btn("Accept draw", () => chessApi.acceptDraw(groupId, gameId), "btn btn--sm");
        btn("Decline", () => chessApi.declineDraw(groupId, gameId));
      } else if (!s.drawOffer) btn("Offer draw", () => chessApi.offerDraw(groupId, gameId));
      btn("Resign", () => chessApi.resign(groupId, gameId), "btn btn--ghost btn--sm", "Resign this game?");
    }
    if (s.status === "completed") btn("Back to lobby", async () => opts.onExit(), "btn btn--sm");

    // moves: "1. e4 e5"
    movesEl.replaceChildren();
    for (let i = 0; i < s.moves.length; i += 2) {
      const li = el("li", "ch-moves__row");
      li.append(el("span", "ch-moves__n", `${s.moves[i].moveNumber}.`), el("span", "", s.moves[i].san), el("span", "", s.moves[i + 1]?.san ?? ""));
      movesEl.append(li);
    }
    movesEl.scrollTop = movesEl.scrollHeight;
  }

  function tickClocks() {
    const s = state;
    if (!s || s.clock.initialSeconds <= 0) return;
    const me = myColor();
    const bottom: Color = me ?? "w";
    const top: Color = bottom === "w" ? "b" : "w";
    const paint = (node: HTMLElement, c: Color) => {
      const ms = clockMs(c);
      node.textContent = ms == null ? "" : fmtClock(ms);
      node.classList.toggle("is-running", s.status === "active" && s.turnColor === c);
      node.classList.toggle("is-low", ms != null && ms < 20_000 && s.status === "active");
    };
    paint(topClock, top);
    paint(botClock, bottom);

    // The side to move ran out. Nobody is moving, so ask the server to end the game (once).
    if (s.status === "active" && me && !timeoutSent) {
      const ms = clockMs(s.turnColor);
      if (ms !== null && ms <= 0) {
        timeoutSent = true;
        void chessApi.timeout(groupId, gameId).then(applyState).catch(() => { timeoutSent = false; void refresh(); });
      }
    }
  }
  const clockTimer = window.setInterval(tickClocks, 100);

  /* ---------- realtime ---------- */
  const onEvent = (e: ChessEvent) => {
    if (e.state) applyState(e.state);
    switch (e.type) {
      case "opponent.connected": if (e.userId !== meUserId) { opponentOnline = true; render(); } break;
      case "opponent.disconnected": if (e.userId !== meUserId) { opponentOnline = false; render(); } break;
      case "draw.offer":
      case "draw.decline": void refresh(); break; // these events carry no state
    }
  };
  // The socket is a speed-up, not a requirement: only players may open it, and if it is down
  // (or refused) the game keeps working by polling the server every few seconds.
  let socket: ChessSocket | null = null;
  let pollTimer: number | undefined;
  const setPolling = (on: boolean) => {
    window.clearInterval(pollTimer);
    pollTimer = undefined;
    if (on) pollTimer = window.setInterval(() => { if (!document.hidden && state?.status !== "completed" && !busy) void refresh(); }, 3000);
  };
  function ensureSocket() {
    if (socket || destroyed || !myColor()) return;
    setPolling(true); // until the socket proves itself
    socket = openChessSocket(groupId, gameId, onEvent, (open) => {
      setPolling(!open);
      if (open) void refresh();
    });
  }
  void refresh();

  return {
    destroy() {
      destroyed = true;
      window.clearInterval(clockTimer);
      socket?.close();
      window.clearInterval(pollTimer);
      resizeObs?.disconnect();
      cg?.destroy();
      container.replaceChildren();
    },
  };
}