// frontend/src/lib/chess/mountChess.ts
// Chess as its own feature: a lobby (start a game, join an open one, resume yours) that
// opens a game view. Works inside a room's central area or on a standalone page.
import { chessApi, type CreateGameInput, type GameSummary } from "../api/chess";
import { describeError } from "../api/client";
import { openGame, type GameHandle } from "./chessGame";
import "../../styles/chess.css";

export interface MountOptions {
  groupId: string;
  roomId?: string;
  meUserId: string;
  announce?(text: string): void;
}
export interface ChessMount { destroy(): void }

const CONTROLS = [
  { key: "none", label: "No clock", sub: "Relaxed", initial: 0, inc: 0 },
  { key: "5+0", label: "5 min", sub: "Blitz", initial: 300, inc: 0 },
  { key: "10+0", label: "10 min", sub: "Rapid", initial: 600, inc: 0 },
  { key: "15+10", label: "15 + 10", sub: "Rapid", initial: 900, inc: 10 },
] as const;

function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls = "", text?: string) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

const controlLabel = (g: { initialSeconds: number; incrementSeconds: number }) =>
  g.initialSeconds === 0 ? "No clock" : `${Math.round(g.initialSeconds / 60)}${g.incrementSeconds ? ` + ${g.incrementSeconds}` : " min"}`;

export function mountChess(container: HTMLElement, opts: MountOptions): ChessMount {
  const { groupId, roomId, meUserId } = opts;
  const storeKey = `rovingr:chess:${meUserId}:${groupId}:${roomId ?? "group"}`;
  let game: GameHandle | null = null;
  let poll: number | undefined;
  let destroyed = false;
  container.classList.add("ch-host"); // enables the container-query layout in chess.css

  const remember = (id: string | null) => { try { id ? sessionStorage.setItem(storeKey, id) : sessionStorage.removeItem(storeKey); } catch { /* ignore */ } };
  const remembered = () => { try { return sessionStorage.getItem(storeKey); } catch { return null; } };

  function stopPoll() { window.clearInterval(poll); poll = undefined; }

  function showGame(gameId: string) {
    stopPoll();
    remember(gameId);
    game?.destroy();
    game = openGame(container, {
      groupId, gameId, meUserId, announce: opts.announce,
      onExit: () => { game?.destroy(); game = null; remember(null); showLobby(); },
    });
  }

  function showLobby() {
    if (destroyed) return;
    container.replaceChildren();
    const root = el("div", "ch-lobby");

    const head = el("div", "ch-lobby__head");
    head.append(el("h2", "ch-lobby__title", "Chess"), el("p", "ch-lobby__lede", "Start a game, or join one that's waiting for an opponent."));

    // --- new game ---
    const form = el("form", "ch-new");
    form.setAttribute("aria-label", "Start a new game");
    const timeSet = el("fieldset", "ch-fieldset");
    timeSet.append(el("legend", "", "Time control"));
    const opts_ = el("div", "ch-choices");
    CONTROLS.forEach((c, i) => {
      const id = `ch-tc-${c.key}`;
      const lab = el("label", "ch-choice"); lab.htmlFor = id;
      const inp = el("input"); inp.type = "radio"; inp.name = "tc"; inp.id = id; inp.value = c.key; inp.checked = i === 1;
      const txt = el("span", "ch-choice__text");
      txt.append(el("strong", "", c.label), el("span", "", c.sub));
      lab.append(inp, txt);
      opts_.append(lab);
    });
    timeSet.append(opts_);

    const colorWrap = el("div", "ch-color");
    const colorLab = el("label", "", "Play as"); colorLab.htmlFor = "ch-color";
    const color = el("select"); color.id = "ch-color";
    for (const [v, t] of [["random", "Random"], ["white", "White"], ["black", "Black"]]) {
      const o = el("option", "", t); o.value = v; color.append(o);
    }
    colorWrap.append(colorLab, color);

    const err = el("p", "ch-error"); err.setAttribute("role", "alert"); err.hidden = true;
    const go = el("button", "btn btn--primary", "Create game"); go.type = "submit";
    form.append(timeSet, colorWrap, go, err);
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const key = (form.querySelector<HTMLInputElement>('input[name="tc"]:checked')?.value) ?? "none";
      const tc = CONTROLS.find((c) => c.key === key) ?? CONTROLS[0];
      const input: CreateGameInput = { roomId, color: color.value as CreateGameInput["color"], initialSeconds: tc.initial, incrementSeconds: tc.inc };
      go.disabled = true; err.hidden = true;
      try { const s = await chessApi.create(groupId, input); showGame(s.id); }
      catch (e2) { err.textContent = describeError(e2); err.hidden = false; go.disabled = false; }
    });

    // --- open + my games ---
    const listBox = el("section", "ch-list");
    listBox.setAttribute("aria-label", "Games");
    const listTitle = el("h3", "ch-list__title", "Games");
    const listBody = el("div", "ch-list__body");
    listBody.append(el("p", "ch-muted", "Looking for games…"));
    listBox.append(listTitle, listBody);

    root.append(head, form, listBox);
    container.append(root);

    async function loadList() {
      let games: GameSummary[];
      try { games = await chessApi.list(groupId, roomId); }
      catch (e) { listBody.replaceChildren(el("p", "ch-error", describeError(e))); return; }
      if (destroyed || !listBody.isConnected) return;
      listBody.replaceChildren();
      if (!games.length) {
        listBody.append(el("p", "ch-muted", "No games waiting. Create one and a member can join you."));
        return;
      }
      const ul = el("ul", "ch-games");
      for (const g of games) {
        const li = el("li", "ch-games__item");
        const host = g.white ?? g.black;
        const who = el("span", "ch-games__who");
        who.append(el("strong", "", g.mine ? "You" : host?.displayName ?? "Member"), el("span", "ch-muted", ` · ${controlLabel(g)}`));
        const b = el("button", "btn btn--sm", ""); b.type = "button";
        if (g.mine) b.textContent = g.status === "pending" ? "Open (waiting)" : "Resume";
        else b.textContent = "Join";
        b.addEventListener("click", async () => {
          b.disabled = true;
          try { if (!g.mine) await chessApi.join(groupId, g.id); showGame(g.id); }
          catch (e) { b.disabled = false; listBody.prepend(el("p", "ch-error", describeError(e))); void loadList(); }
        });
        li.append(who, b);
        ul.append(li);
      }
      listBody.append(ul);
    }
    void loadList();
    stopPoll();
    poll = window.setInterval(() => { if (!document.hidden) void loadList(); }, 5000);
  }

  // Reload-friendly: if I was in a game, go straight back to it (the server keeps the truth).
  const resume = remembered();
  if (resume) {
    chessApi.get(groupId, resume).then((s) => { if (!destroyed) showGame(s.id); }).catch(() => { remember(null); showLobby(); });
  } else showLobby();

  return {
    destroy() {
      destroyed = true;
      stopPoll();
      game?.destroy();
      container.replaceChildren();
    },
  };
}