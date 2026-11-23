// frontend: src/lib/stream/types.ts
// Wire format shared with backend/internal/stream/handlers.go

export type StreamRole = "host" | "viewer";

export interface RosterEntry {
  connectionId: string;
  userId: string;
  role: StreamRole; // effective role: "host" only for the one publisher
  joinedAt: string;
}

export interface StreamStatePayload {
  roomId: string;
  hostConnId: string; // "" when nobody is publishing
  hostCamera: boolean; // the host says their camera is on
  participants: RosterEntry[];
}

/** Sent only to the joining connection: the role the SERVER decided you have. */
export interface StreamJoinedPayload {
  roomId: string;
  role: StreamRole;
  connectionId: string;
}

export interface SignalPayload {
  roomId: string;
  to: string;
  from?: string; // stamped by the server on relay
  kind: "offer" | "answer" | "ice";
  data: unknown;
}
