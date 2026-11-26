// frontend/src/lib/chess/chessPage.ts: standalone page at /groups/:id/chess
import { requireUser } from "../auth/session";
import { mountChess } from "./mountChess";

export async function initChessPage() {
  const root = document.querySelector<HTMLElement>("[data-ch-page]");
  const mount = document.querySelector<HTMLElement>("[data-ch-mount]");
  if (!root || !mount) return;
  const user = await requireUser(); // redirects to /login on 401
  mountChess(mount, { groupId: root.dataset.groupId!, meUserId: user.id });
}