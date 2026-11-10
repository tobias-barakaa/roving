import { groupsApi, type Group, type GroupRole } from "../api/groups";

/** One GET /groups/:id per page, shared by the header, rooms, members, etc. */
const cache = new Map<string, Promise<Group>>();

export function getGroup(id: string): Promise<Group> {
  let p = cache.get(id);
  if (!p) {
    p = groupsApi.get(id);
    p.catch(() => cache.delete(id));
    cache.set(id, p);
  }
  return p;
}
export function reloadGroup(id: string) { cache.delete(id); return getGroup(id); }

/** UNCONFIRMED field names (role / myRole): if neither exists the role is unknown and no admin UI is shown. */
// export const roleOf = (g: Group | null | undefined): GroupRole | undefined => g?.role ?? g?.myRole;
export const roleOf = (g: Group | null | undefined): GroupRole | undefined =>
  g?.role;

/** UX only. The backend enforces every one of these. */
export const canCreateRooms = (r?: GroupRole) => r === "owner" || r === "admin" || r === "moderator";
export const canManageGroup = (r?: GroupRole) => r === "owner" || r === "admin";
export const roleLabel = (r?: string) => (r ? r.charAt(0).toUpperCase() + r.slice(1) : "");
