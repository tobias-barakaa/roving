import { ApiError } from "../api/client";
import type { Room } from "../api/room";

/**
 * What the Room page should show. UX only: the backend decides who may enter.
 *
 *  - missing : GET returned 403/404. We can't tell "wrong id" from "no access", and we shouldn't try.
 *  - error   : network / 5xx etc. Retry makes sense.
 *  - blocked : the room exists but canEnter is not true.
 *  - enter   : canEnter is true.
 *
 * canEnter is optional in the Room type and Go may omit a false boolean, so anything other than
 * a literal `true` is treated as "cannot enter".
 */
export type BlockedReason = "restricted" | "group" | "archived";

export type RoomView =
  | { kind: "missing" }
  | { kind: "error"; status: number }
  | { kind: "blocked"; room: Room; reason: BlockedReason }
  | { kind: "enter"; room: Room };

export function viewForRoom(room: Room): RoomView {
  if (room.archivedAt) return { kind: "blocked", room, reason: "archived" };
  if (room.canEnter === true) return { kind: "enter", room };
  return { kind: "blocked", room, reason: room.access === "restricted" ? "restricted" : "group" };
}

export function viewForError(err: unknown): RoomView {
  if (err instanceof ApiError && (err.status === 403 || err.status === 404)) return { kind: "missing" };
  return { kind: "error", status: err instanceof ApiError ? err.status : -1 };
}

export const blockedCopy: Record<BlockedReason, { title: string; text: string }> = {
  restricted: {
    title: "This room is restricted",
    text: "Only people a room host has added can enter. Ask a host of this room to give you access, then come back.",
  },
  group: {
    title: "You can't enter this room right now",
    text: "This room is open to the group, but your account isn't allowed in at the moment. Check that you're still a member of the group, or ask a group admin.",
  },
  archived: {
    title: "This room is archived",
    text: "Archived rooms are closed. Ask a group admin if you think it should be open again.",
  },
};