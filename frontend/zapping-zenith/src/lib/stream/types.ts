// frontend/src/lib/stream/types.ts
export interface RosterEntry {
  connectionId: string;
  userId: string;
  role: "host" | "viewer";
  joinedAt: string;
}

export interface StreamStatePayload {
  roomId: string;
  hostConnId: string;
  participants: RosterEntry[];
}

export interface SignalPayload {
  roomId: string;
  to: string;
  kind: "offer" | "answer" | "ice";
  data: unknown;
  from?: string;
}