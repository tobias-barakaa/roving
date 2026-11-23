/** Wire format shared with backend/internal/realtime/protocol.go */
export interface Envelope<P = unknown> {
  type: string;
  id?: string;
  payload?: P;
}

export interface HelloPayload {
  connectionId: string;
  userId: string;
  serverTime: string;
  maxMessageBytes: number;
}

export interface ErrorPayload { code: string; message: string }

export type RealtimeStatus =
  | "idle"          // not started
  | "connecting"
  | "open"
  | "reconnecting"  // waiting to retry
  | "closed"        // stopped on purpose, or the session ended
  | "unauthorized"; // the server says you are logged out
