import { groupsApi, type Group, type GroupRole } from "../api/groups";
import type { User } from "../api/auth";
import { getGroup } from "./context";

/**
 * Works out what the signed-in user may do in a group, for UX only.
 * The Go backend enforces every action; this just decides which buttons to show.
 *
 * Why this exists: the groups DTO was never shared, so the key that carries
 * "my role in this group" is not known. Reading one guessed key made the role
 * undefined, which hid Create Room for everyone. Instead we:
 *   1. look for a role in GET /groups/:id (several key names, any casing, nested membership)
 *   2. then in this group's entry in GET /groups (the "mine" list)
 *   3. then in this user's row of GET /groups/:id/members
 * and if none of them says, the answer is "unknown", not "no".
 */
export type Perm = "yes" | "no" | "unknown";
export type RoleSource = "detail" | "mine" | "members" | "unknown";

export interface GroupAccess {
  role?: GroupRole;
  source: RoleSource;
  canCreateRooms: Perm;
  canManageGroup: Perm;
}

/** Create Room is shown unless we positively know the person is a plain member. */
export const showCreateRoom = (a: GroupAccess) => a.canCreateRooms !== "no";
/** Admin-only actions need a positive answer. */
export const showManageActions = (a: GroupAccess) => a.canManageGroup === "yes";

const ROLE_KEYS = ["role", "myRole", "memberRole", "currentUserRole", "viewerRole", "userRole", "myGroupRole", "groupRole", "callerRole"];
const NESTED_KEYS = ["membership", "myMembership", "currentMembership", "me", "viewer"];

export function normaliseRole(v: unknown): GroupRole | undefined {
  if (typeof v !== "string") return undefined;
  switch (v.trim().toLowerCase()) {
    case "owner": return "owner";
    case "admin":
    case "administrator": return "admin";
    case "moderator":
    case "mod": return "moderator";
    case "member": return "member";
    default: return undefined;
  }
}

function roleFrom(obj: unknown, depth = 0): GroupRole | undefined {
  if (!obj || typeof obj !== "object" || depth > 2) return undefined;
  const o = obj as Record<string, unknown>;
  for (const k of ROLE_KEYS) {
    const r = normaliseRole(o[k]);
    if (r) return r;
  }
  for (const k of NESTED_KEYS) {
    const v = o[k];
    const r = typeof v === "string" ? normaliseRole(v) : roleFrom(v, depth + 1);
    if (r) return r;
  }
  return undefined;
}

function build(role: GroupRole | undefined, source: RoleSource): GroupAccess {
  if (!role) return { source: "unknown", canCreateRooms: "unknown", canManageGroup: "unknown" };
  const staff = role === "owner" || role === "admin";
  return {
    role,
    source,
    canCreateRooms: staff || role === "moderator" ? "yes" : "no",
    canManageGroup: staff ? "yes" : "no",
  };
}

async function resolve(groupId: string, user: User): Promise<GroupAccess> {
  const group: Group = await getGroup(groupId); // if the group itself can't load, callers handle that

  let role = roleFrom(group);
  let source: RoleSource = "detail";

  if (!role) {
    try {
      const mine = await groupsApi.mine();
      role = roleFrom(mine.items.find((g) => g.id === groupId));
      source = "mine";
    } catch { /* fall through */ }
  }
  if (!role) {
    try {
      const members = await groupsApi.members(groupId);
      const me = members.find((m) => m.userId === user.id);
      role = normaliseRole(me?.role);
      source = "members";
    } catch { /* fall through */ }
  }

  if (!role) {
    console.warn(
      "[rovingr] Couldn't tell your role in this group. GET /groups/:id returned these keys:",
      Object.keys(group),
      "- showing Create Room anyway; the server enforces permissions. Tell the frontend which key holds the role.",
    );
  } else if (source !== "detail") {
    console.info(`[rovingr] Role "${role}" came from ${source}; GET /groups/:id did not include it.`);
  }
  return build(role, source);
}

const cache = new Map<string, Promise<GroupAccess>>();

export function resolveAccess(groupId: string, user: User): Promise<GroupAccess> {
  const key = `${groupId}:${user.id}`;
  let p = cache.get(key);
  if (!p) {
    p = resolve(groupId, user);
    p.catch(() => cache.delete(key));
    cache.set(key, p);
  }
  return p;
}

/** Same as resolveAccess but never throws: if the group can't be read, the answer is "unknown". */
export async function resolveAccessSafe(groupId: string, user: User): Promise<GroupAccess> {
  try { return await resolveAccess(groupId, user); } catch { return build(undefined, "unknown"); }
}
