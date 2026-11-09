import { api, type Page } from "./client";
import { endpoints } from "./endpoints";

export type Visibility = "public" | "private";
export type Membership = "member" | "pending" | "invited" | "none";
export type GroupRole = "owner" | "admin" | "moderator" | "member";

/** UNCONFIRMED shape: every field except id is optional until checked against the Go DTO. */
export interface Group {
  id: string;
  name?: string;
  slug?: string;
  description?: string;
  visibility?: Visibility;
  memberCount?: number;
  membership?: Membership;
  role?: GroupRole;
}

export interface CreateGroupInput {
  name: string;
  description: string;
  visibility: Visibility;
}

export const groupsApi = {
  /** Public groups for discovery. Private groups are also filtered out in the UI as a second guard. */
  discover: (q = "") => {
    const p = new URLSearchParams({ visibility: "public" });
    if (q) p.set("q", q);
    return api.get<Page<Group>>(`${endpoints.groups.list}?${p}`);
  },
  mine: () => api.get<Page<Group>>(`${endpoints.groups.list}?membership=mine`),
  get: (id: string) => api.get<Group>(endpoints.groups.detail(id)),
  create: (input: CreateGroupInput) => api.post<Group>(endpoints.groups.create, input),
  join: (id: string) => api.post<unknown>(endpoints.groups.join(id)),
};
