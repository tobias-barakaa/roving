
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
}



export interface GroupListResponse {
  groups: Group[];
}

export interface CreateGroupInput {
  name: string;
  description: string;
  visibility: Visibility;
}
export interface GroupMember {
  userId: string;
  displayName: string;
  email: string;
  role: GroupRole;
  status: string;
  joinedAt?: string | null;
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
