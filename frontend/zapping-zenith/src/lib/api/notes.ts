// frontend: src/lib/api/notes.ts   (matches internal/notes/dto.go)
import { api } from "./client";
import { endpoints } from "./endpoints";

export type NoteVisibility = "private" | "group";

export interface Note {
  id: string;
  groupId?: string;
  title: string;
  body: string;
  visibility: NoteVisibility;
  pinned: boolean;
  tags: string[];
  authorName?: string;
  isMine: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface CreateNoteInput {
  groupId?: string;
  title: string; // <= 200
  body: string; // <= 50000
  visibility?: NoteVisibility;
}

export interface UpdateNoteInput {
  title?: string;
  body?: string;
  visibility?: NoteVisibility;
}

export const notesApi = {
  listMine: async (o: { groupId?: string; q?: string; limit?: number } = {}): Promise<Note[]> => {
    const p = new URLSearchParams();
    if (o.groupId) p.set("groupId", o.groupId);
    if (o.q) p.set("q", o.q);
    if (o.limit) p.set("limit", String(o.limit));
    const qs = p.toString();
    const res = await api.get<{ notes: Note[] }>(`${endpoints.notes.mine}${qs ? `?${qs}` : ""}`);
    return res.notes ?? [];
  },
  listShared: async (groupId: string, limit = 20): Promise<Note[]> => {
    const res = await api.get<{ notes: Note[] }>(`${endpoints.notes.shared(groupId)}?limit=${limit}`);
    return res.notes ?? [];
  },
  create: (input: CreateNoteInput) => api.post<Note>(endpoints.notes.mine, input),
  /** `keepalive` lets the last autosave finish while the page is closing. */
  update: (id: string, input: UpdateNoteInput, keepalive = false) =>
    api.patch<void>(endpoints.notes.one(id), input, keepalive ? { keepalive: true } : undefined),
};
