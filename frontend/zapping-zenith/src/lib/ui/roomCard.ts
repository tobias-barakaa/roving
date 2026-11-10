import type { Room } from "../api/room";
import { groupPaths } from "../groups/routes";

export type Access = "yes" | "no" | "checking";

function t<K extends keyof HTMLElementTagNameMap>(tag: K, cls: string, text?: string) {
  const e = document.createElement(tag);
  e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

/**
 * Safe DOM (textContent only). Facts shown are only those the backend provides:
 * access mode, optional capacity, whether you created it, and canEnter.
 */
export function roomCard(groupId: string, room: Room, access: Access, mine: boolean): HTMLElement {
  const restricted = room.access === "restricted";
  const card = t("article", `gx-room ${restricted ? "gx-room--restricted" : "gx-room--open"}`);
  card.dataset.roomId = room.id;

  const body = t("div", "gx-room__body");
  const badges = t("p", "gx-room__badges");
  badges.append(t("span", `gx-badge ${restricted ? "gx-badge--muted" : "gx-badge--ok"}`, restricted ? "Restricted" : "Open to group"));
  if (room.myRoomRole === "host") badges.append(t("span", "gx-badge", "You host this room"));
  body.append(badges);
  body.append(t("h3", "gx-room__title", room.name));
  if (room.description) body.append(t("p", "gx-room__desc", room.description));

  const meta: string[] = [];
  if (typeof room.capacity === "number") meta.push(`Up to ${room.capacity} people`);
  if (mine) meta.push("Created by you");
  if (meta.length) body.append(t("p", "gx-room__meta", meta.join(" · ")));

  const side = t("div", "gx-room__side");
  if (access === "yes") {
    const a = t("a", "btn btn--secondary btn--sm", "Enter room");
    a.href = groupPaths(groupId).room(room.id);
    a.setAttribute("aria-label", `Enter room ${room.name}`);
    side.append(a);
  } else if (access === "checking") {
    side.append(t("span", "gx-room__note", "Checking access…"));
  } else {
    side.append(t("span", "gx-room__note", "Ask a room host for access"));
  }

  card.append(body, side);
  return card;
}
