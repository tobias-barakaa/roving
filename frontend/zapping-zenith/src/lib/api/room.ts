import { api } from "./client";
import { endpoints } from "./endpoints";

/** Matches rooms/dto.go exactly. */
export type RoomAccess = "group" | "restricted";
export type RoomRole = "member" | "host";

export interface Room {
  id: string;
  groupId: string;
  name: string;
  description: string;
  access: RoomAccess;
  capacity?: number;
  archivedAt?: string;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  /** Only on single-room reads (GET /groups/:id/rooms/:rid): */
  groupName?: string;
  myRoomRole?: RoomRole;
  canEnter?: boolean;
}

export interface CreateRoomInput {
  name: string; // 1..100
  description: string; // 0..2000
  access: RoomAccess;
  capacity?: number; // 1..1000
}

export interface ListRoomsOptions {
  includeArchived?: boolean;
  access?: RoomAccess;
  search?: string;
  limit?: number; // 1..200 (backend default 50)
  offset?: number;
}

export const roomsApi = {
  list: async (groupId: string, o: ListRoomsOptions = {}): Promise<Room[]> => {
    const p = new URLSearchParams();
    if (o.includeArchived) p.set("includeArchived", "true");
    if (o.access) p.set("access", o.access);
    if (o.search) p.set("search", o.search);
    if (o.limit) p.set("limit", String(o.limit));
    if (o.offset) p.set("offset", String(o.offset));
    const qs = p.toString();
    const res = await api.get<{ rooms: Room[] }>(`${endpoints.rooms.list(groupId)}${qs ? `?${qs}` : ""}`);
    return res.rooms ?? [];
  },
  /** Single read: the only call that returns canEnter and myRoomRole. */
  get: (groupId: string, roomId: string) => api.get<Room>(endpoints.rooms.detail(groupId, roomId)),
  create: (groupId: string, input: CreateRoomInput) => api.post<Room>(endpoints.rooms.create(groupId), input),
};
