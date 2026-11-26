// frontend/src/lib/chess/chessTab.ts
// Chess as the "Chess" tab of the room's side panel. It watches the tab instead of
// patching roomPage's tab code: when the tab becomes selected, chess mounts (once)
// and the panel widens so the board has room. Leaving the tab keeps the game and
// its clocks alive; the server holds the truth anyway.
import { mountChess, type ChessMount, type MountOptions } from "./mountChess";

export interface ChessTabHandle { destroy(): void }

export function initChessTab(root: HTMLElement, opts: MountOptions): ChessTabHandle {
  const tab = root.querySelector<HTMLButtonElement>('[data-rm-tab="chess"]');
  const mount = root.querySelector<HTMLElement>("[data-ch-mount]");
  const layout = root.querySelector<HTMLElement>("[data-rm-layout]");
  if (!tab || !mount) return { destroy() {} };

  let chess: ChessMount | null = null;
  const sync = () => {
    const selected = tab.getAttribute("aria-selected") === "true";
    layout?.classList.toggle("has-chess", selected);
    if (selected && !chess) chess = mountChess(mount, opts);
  };
  const obs = new MutationObserver(sync);
  obs.observe(tab, { attributes: true, attributeFilter: ["aria-selected"] });

  // Any "Play chess" card/button in the page just opens the tab.
  const openers = Array.from(root.querySelectorAll<HTMLElement>("[data-rm-open-chess]"));
  const open = () => {
    const side = root.querySelector<HTMLElement>("[data-rm-side]");
    if (side?.hidden) root.querySelector<HTMLButtonElement>("[data-rm-side-toggle]")?.click(); // reveal the panel first
    tab.click();
  };
  openers.forEach((b) => b.addEventListener("click", open));

  sync();
  return {
    destroy() {
      obs.disconnect();
      openers.forEach((b) => b.removeEventListener("click", open));
      chess?.destroy();
      chess = null;
      layout?.classList.remove("has-chess");
    },
  };
}