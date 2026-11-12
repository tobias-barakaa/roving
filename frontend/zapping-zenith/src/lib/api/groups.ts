
import { api } from "./client";
import { endpoints } from "./endpoints";

export type Visibility = "public" | "private";
export type Membership = "member" | "pending" | "invited" | "none";
export type GroupRole = "owner" | "admin" | "moderator" | "member";

// export interface Group {
//   id: string;
//   name?: string;
//   slug?: string;
//   description?: string;
//   visibility?: Visibility;
//   memberCount?: number;
//   membership?: Membership;
//   role?: GroupRole;
  
// }

export interface Group {
  id: string;
  name?: string;
  slug?: string;
  description?: string;
  visibility?: Visibility;
  memberCount?: number;
  membership?: Membership;
  role?: GroupRole;
  myRole?: GroupRole;
  createdAt?: string;
  updatedAt?: string;
  createdBy?: string;
  [key: string]: unknown;
}




// export interface GroupListResponse {
//   groups: Group[];
// }

export interface CreateGroupInput {
  name: string;
  description: string;
  visibility: Visibility;
}
export interface GroupMember {
  userId: string;
  displayName: string;
  email?: string;
  role: GroupRole;
  status: string;
  joinedAt?: string | null;
}



export interface GroupListResponse {
  items: Group[];
  nextCursor: string | null;
}
// export interface CreateGroupInput {
//   name: string;
//   description: string;
//   visibility: Visibility;
// }

// export interface CreateGroupInput {
//   name: string;
//   slug: string;
//   description: string;
//   visibility: Visibility;
// }

export const groupsApi = {
  // GET /groups/public
  discover: (q = "") => {
    const params = new URLSearchParams();

    if (q) params.set("q", q);

    const query = params.toString();

    return api.get<GroupListResponse>(
      `${endpoints.groups.public}${query ? `?${query}` : ""}`
    );

    
  },

  // GET /groups
  mine: () =>
    api.get<GroupListResponse>(endpoints.groups.mine),

  // GET /groups/:id
  get: (id: string) =>
    api.get<Group>(endpoints.groups.detail(id)),

  // POST /groups
  create: (input: CreateGroupInput) =>
    api.post<Group>(endpoints.groups.create, input),

  // POST /groups/:id/join
  join: (id: string) =>
    api.post<unknown>(endpoints.groups.join(id)),


    leave: (id: string) =>
    api.post<unknown>(`${endpoints.groups.detail(id)}/leave`),

    // GET /groups/:id/members
    members: async (id: string): Promise<GroupMember[]> => {
      const response = await api.get<{ members: GroupMember[] }>(
        `${endpoints.groups.detail(id)}/members`
      );
  
      return response.members ?? [];
    },

};



/** Accepts a bare array, {items}, or {groups}; throws if the body is none of those. */
function toGroups(raw: unknown): { items: Group[]; nextCursor: null } {
  const r = raw as { items?: unknown; groups?: unknown } | unknown[] | null;
  const arr = Array.isArray(r) ? r : Array.isArray(r?.items) ? r.items : Array.isArray(r?.groups) ? r.groups : null;
  if (!arr) throw new Error("Unexpected response from the groups API");
  return { items: arr as Group[], nextCursor: null };
}

function toMembers(raw: unknown): GroupMember[] {
  const r = raw as { members?: unknown; items?: unknown } | unknown[] | null;

  const arr = Array.isArray(r)
    ? r
    : Array.isArray(r?.members)
      ? r.members
      : Array.isArray(r?.items)
        ? r.items
        : null;

  if (!arr) throw new Error("Unexpected response from the members API");

  return (arr as Record<string, unknown>[]).map((m) => ({
    userId: (m.userId ?? m.id) as string,
    displayName: m.displayName as string,
    email: m.email as string,
    role: m.role as GroupRole,
    status: m.status as string,
    joinedAt: (m.joinedAt as string | null | undefined) ?? null,
  }));
}

/** Accepts the group directly, or wrapped as {group: {...}, ...siblings}. */
function toGroup(raw: unknown): Group {
  const r = raw as Record<string, unknown> | null;
  if (r && typeof r.group === "object" && r.group && !Array.isArray(r.group)) {
    return { ...r, ...(r.group as Record<string, unknown>) } as Group;
  }
  return raw as Group;
}

// export const groupsApi = {
//   /** Public groups for discovery. The `q` param is UNCONFIRMED; private groups are also filtered in the UI. */
//   discover: async (q = "") =>
//     toGroups(await api.get<unknown>(`${endpoints.groups.publicList}${q ? `?q=${encodeURIComponent(q)}` : ""}`)),
//   mine: async () => toGroups(await api.get<unknown>(endpoints.groups.mine)),
//   get: async (id: string) => toGroup(await api.get<unknown>(endpoints.groups.detail(id))),
//   members: async (id: string) => toMembers(await api.get<unknown>(endpoints.groups.members(id))),
//   create: async (input: CreateGroupInput) => toGroup(await api.post<unknown>(endpoints.groups.create, input)),
//   join: (id: string) => api.post<unknown>(endpoints.groups.join(id)),
//   leave: (id: string) => api.post<void>(endpoints.groups.leave(id)),
// };



// import { api, type Page } from "./client";
// import { endpoints } from "./endpoints";

// export type Visibility = "public" | "private";
// export type Membership = "member" | "pending" | "invited" | "none";
// export type GroupRole = "owner" | "admin" | "moderator" | "member";

// /** UNCONFIRMED shape: every field except id is optional until checked against the Go DTO. */
// export interface Group {
//   id: string;
//   name?: string;
//   slug?: string;
//   description?: string;
//   visibility?: Visibility;
//   memberCount?: number;
//   membership?: Membership;
//   role?: GroupRole;
// }

// export interface CreateGroupInput {
//   name: string;
//   description: string;
//   visibility: Visibility;
// }

// export const groupsApi = {
//   /** Public groups for discovery. Private groups are also filtered out in the UI as a second guard. */
//   discover: (q = "") => {
//     const p = new URLSearchParams({ visibility: "public" });
//     if (q) p.set("q", q);
//     return api.get<Page<Group>>(`${endpoints.groups.list}?${p}`);
//   },
//   mine: () => api.get<Page<Group>>(`${endpoints.groups.list}?membership=mine`),
//   get: (id: string) => api.get<Group>(endpoints.groups.detail(id)),
//   create: (input: CreateGroupInput) => api.post<Group>(endpoints.groups.create, input),
//   join: (id: string) => api.post<unknown>(endpoints.groups.join(id)),
// };
