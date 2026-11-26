// frontend/src/lib/api/chess.ts
import { api } from "./client";
import { endpoints } from "./endpoints";

export type Color = "w" | "b";
export type GameStatus = "pending" | "active" | "completed";

export interface ChessPlayer { userId: string; displayName: string }
export interface ChessMove { color: Color; san: string; uci: string; moveNumber: number; ply: number; givesCheck: boolean }

export interface ChessState {
  id: string;
  groupId: string;
  roomId: string | null;
  status: GameStatus;
  result: string | null;
  resultMethod: string | null;
  white: ChessPlayer | null;
  black: ChessPlayer | null;
  fen: string;
  turnColor: Color;
  lastMove: string[] | null;
  check: Color | null;
  dests: Record<string, string[]> | null;
  drawOffer: Color | null;
  clock: {
    initialSeconds: number;
    incrementSeconds: number;
    whiteRemainingMs: number | null;
    blackRemainingMs: number | null;
    startedAt: string | null;
    turnColor: Color;
  };
  moves: ChessMove[];
  version: number;
  serverTime?: string;
  createdAt: string;
  startedAt: string | null;
  completedAt: string | null;
}

export interface GameSummary {
  id: string;
  status: GameStatus;
  roomId: string | null;
  white: ChessPlayer | null;
  black: ChessPlayer | null;
  initialSeconds: number;
  incrementSeconds: number;
  createdBy: string;
  createdAt: string;
  mine: boolean;
}

export interface CreateGameInput {
  roomId?: string;
  color: "white" | "black" | "random";
  initialSeconds: number;
  incrementSeconds: number;
}

export interface MoveInput { orig: string; dest: string; promotion: string; version: number }

const post = <T>(path: string, body?: unknown) => api.post<T>(path, body ?? {});

export const chessApi = {
  list: async (groupId: string, roomId?: string): Promise<GameSummary[]> => {
    const qs = roomId ? `?roomId=${encodeURIComponent(roomId)}` : "";
    const res = await api.get<{ games: GameSummary[] }>(`${endpoints.chess.list(groupId)}${qs}`);
    return res.games ?? [];
  },
  create: (groupId: string, input: CreateGameInput) => post<ChessState>(endpoints.chess.list(groupId), input),
  get: (groupId: string, gameId: string) => api.get<ChessState>(endpoints.chess.game(groupId, gameId)),
  join: (groupId: string, gameId: string) => post<ChessState>(endpoints.chess.action(groupId, gameId, "join")),
  cancel: (groupId: string, gameId: string) => post<{ ok: boolean }>(endpoints.chess.action(groupId, gameId, "cancel")),
  move: (groupId: string, gameId: string, m: MoveInput) =>
    post<{ accepted: boolean; state: ChessState }>(endpoints.chess.action(groupId, gameId, "move"), m),
  timeout: (groupId: string, gameId: string) => post<ChessState>(endpoints.chess.action(groupId, gameId, "timeout")),
  resign: (groupId: string, gameId: string) => post<ChessState>(endpoints.chess.action(groupId, gameId, "resign")),
  offerDraw: (groupId: string, gameId: string) => post<ChessState>(endpoints.chess.action(groupId, gameId, "draw/offer")),
  acceptDraw: (groupId: string, gameId: string) => post<ChessState>(endpoints.chess.action(groupId, gameId, "draw/accept")),
  declineDraw: (groupId: string, gameId: string) => post<ChessState>(endpoints.chess.action(groupId, gameId, "draw/decline")),
};