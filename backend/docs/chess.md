# Rovingr Chess Backend — Investigation, Design and API Contract

This document records the Stage 1–4 investigation and design, and the final API
contract (Stage 6) that the future Astro frontend will consume. The backend is
implemented in `internal/chess` and wired in `cmd/api/main.go`; the migration is
`migrations/0017_chess.sql`.

---

## 1. Repository investigation

### 1.1 Chessground (`lichess-org/chessground`)

Chessground is a **GPL-3.0** TypeScript board renderer. It contains **no chess
rules, no game management, no networking** — only board rendering, interaction
and callbacks.

Key facts verified from the actual source (`src/api.ts`, `src/config.ts`,
`src/board.ts`, `src/types.ts`, `src/fen.ts`, `README.md`):

- A square is a `Key` like `"e2"` (`File` a–h + `Rank` 1–8).
- A user move is reported through two callbacks:
  - `movable.events.after?: (orig: Key, dest: Key, metadata: MoveMetadata) => void`
    — **this is the one to use**. `orig`/`dest` are the origin/destination
    squares. `MoveMetadata = { premove, ctrlKey?, holdTime?, captured?, predrop? }`.
  - `events.move?: (orig: Key, dest: Key, capturedPiece?: Piece) => void` — a
    lower-level hook fired inside `baseMove`.
- Legal destinations are supplied as `movable.dests?: Map<Key, Key[]>`; a move is
  allowed only when `dests.get(orig)?.includes(dest)`. Playing a move clears
  `dests` and flips `turnColor`.
- The board is updated by `set(config)` (or individual API methods); `set` with
  `fen`, `turnColor`, `lastMove`, `check` and `movable.dests` is how the backend
  drives the board.
- `getFen()` returns **pieces only** (`fenWrite(state.pieces)`), e.g.
  `rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR` — **no turn, castling, en
  passant or half-move clock**. It is *not* an authoritative FEN. Our backend
  returns the full FEN from the rules library instead.
- **Promotion**: Chessground has no promotion picker and no promotion field in
  its callbacks. The app must detect a pawn reaching the last rank and prompt
  the user, then send `orig`/`dest`/`promotion` to the backend.
- Reverting a rejected move: there is no "undo a completed move" API. The
  frontend re-syncs with `set({ fen, turnColor, lastMove, check, movable:
  { dests } })` using the authoritative state the backend returns.
- Config covers `orientation`, `turnColor`, `check` (a colour or boolean),
  `lastMove`, `highlight`, `movable.color`, `viewOnly`, `draggable`,
  `selectable`, `animation`, `premovable`, etc.
- License: **GPL-3.0** (see `README.md`), which has implications for the
  frontend distribution — flag this before shipping the Astro site.

### 1.2 Go chess rules libraries

| Library | Module | Licence | Notes |
|---|---|---|---|
| **CorentinGS/chess** | `github.com/corentings/chess/v2` | MIT | Fork of the very widely used `notnil/chess`. Full rules, FEN/PGN/SAN/UCI/LAN, bitboards, active maintenance. |
| **idylicaro/go-chess** | `github.com/idylicaro/go-chess` | MIT | Modern, clean API; adds a chess clock, draw offers, `Undo`, perft tests. Newer/smaller user base. |
| **dylhunn/dragontoothmg** | (unverified — README fetch returned 404) | — | A low-level bitboard move generator (perft-focused). No PGN/game management/clock, long unmaintained. Not appropriate. |

**Recommendation: `github.com/corentings/chess/v2`** (CorentinGS/chess).

Reasoning:

- Battle-tested lineage (`notnil/chess`), full rule coverage and notation, MIT
  licence, active maintenance, clean public API (`Game`, `Position`, `Move`,
  `Square`, `Color`, `Piece`, `PieceType`).
- It provides exactly what the backend must own: legal move generation,
  validation, check/checkmate, stalemate, castling, en passant, promotion, and
  draw detection (threefold, fivefold, fifty-move, seventy-five-move,
  insufficient material) plus resignation and draw offers.
- `idylicaro/go-chess` is a strong alternative (its built-in clock is nice), but
  our clock must be persisted in PostgreSQL and controlled by the backend
  anyway, so a library clock is not decisive. CorentinGS wins on maturity and
  breadth.

`dragontoothmg` is a move-generation library, not a game/state/rules library,
and is unsuitable here.

### 1.3 WebSocket library

The project already depends on **`github.com/gorilla/websocket v1.5.3`** and its
`internal/realtime` package already demonstrates the exact patterns we need
(origin check, cookie auth, ping/pong, per-connection write pump, graceful
shutdown). `github.com/coder/websocket` is a good modern alternative (context
support, `wsjson`, concurrent writes), but adding a second WebSocket library is
unjustified. **We reuse gorilla/websocket** for the chess game socket.

---

## 2. Architecture

Responsibilities are split exactly as requested:

- **Chessground** (future frontend) — render, detect input, report proposed
  moves, render authoritative positions.
- **`internal/chess/engine.go`** — adapter around CorentinGS/chess. The only file
  that imports the rules library. Validates moves, generates legal moves,
  produces authoritative FEN + UCI/SAN + check flag + outcome.
- **`internal/chess/service.go`** — game orchestration: create/join, turn and
  authorization checks, move submission, clocks, resignation, draw offers,
  persistence via transactions, fan-out after commit.
- **`internal/chess/repository.go`** — PostgreSQL access.
- **`internal/chess/clock.go`** — Fischer time-control math (untimed or
  initial + increment).
- **`internal/chess/websocket.go`** — per-game connection hub (gorilla/websocket).
- **`internal/chess/handler.go`** — HTTP routes + WebSocket upgrade.
- **`internal/chess/{model,dto}.go`** — domain types, errors, request/response DTOs.

The backend is authoritative. The frontend never decides legality; it only
reports `orig`/`dest`/`promotion` and renders whatever state the backend returns.

Authentication reuses `auth.RequireAuth` (the existing `rovingr_session`
cookie). Authorization reuses the existing group/room membership tables through
the same access-gate pattern as `chat`/`calls` (non-member → 404
anti-enumeration, suspended → 403, hidden restricted room → 404, archived room
is read-only).

---

## 3. HTTP API

All routes are under `/api/v1` and require the session cookie (`RequireAuth`).
The group id is always the URL's `:groupId`. A game is scoped to that group and
optionally to one of its rooms.

| # | Method | URL | Purpose |
|---|--------|-----|---------|
| 1 | POST | `/groups/:groupId/chess/games` | Create a game (creator is white/random/black) |
| 2 | GET | `/groups/:groupId/chess/games/:id` | Retrieve the full authoritative state |
| 3 | POST | `/groups/:groupId/chess/games/:id/join` | Join as the opponent |
| 4 | POST | `/groups/:groupId/chess/games/:id/move` | Submit a move |
| 5 | GET | `/groups/:groupId/chess/games/:id/moves` | Retrieve move history |
| 6 | GET | `/groups/:groupId/chess/games/:id/legal-moves` | Retrieve legal destinations |
| 7 | POST | `/groups/:groupId/chess/games/:id/resign` | Resign |
| 8 | POST | `/groups/:groupId/chess/games/:id/draw/offer` | Offer a draw |
| 9 | POST | `/groups/:groupId/chess/games/:id/draw/accept` | Accept a draw |
| 10 | POST | `/groups/:groupId/chess/games/:id/draw/decline` | Decline a draw |
| 11 | GET | `/groups/:groupId/chess/games/:id/ws` | Connect to the game's WebSocket |

Common error shape: `{"error": "<message>", "code": "<code>"}`. Codes:
`not_found`, `forbidden`, `not_a_player`, `invalid_request`, `illegal_move`,
`promotion_required`, `not_your_turn`, `stale_move`, `game_over`, `game_full`,
`already_joined`.

### 3.1 Create a game

- `POST /groups/:groupId/chess/games`
- Request:
  ```json
  { "roomId": "uuid|null", "color": "white|black|random", "initialSeconds": 300, "incrementSeconds": 3 }
  ```
- Response: `201` with a `GameStateResponse` (status `pending`).
- Errors: `400 invalid_request`, `403 forbidden`, `404 not_found`.
- Service: `Service.Create`; uses `Engine.InitialFEN` + `ClockConfig.InitialMs`.

### 3.2 Retrieve a game / move history / legal moves

- `GET .../games/:id` → `200` `GameStateResponse` (`Service.Get`).
- `GET .../games/:id/moves` → `200` `{"moves": [...]}`.
- `GET .../games/:id/legal-moves` → `200`:
  ```json
  { "fen": "...", "turnColor": "w|b", "check": "w|b|null", "dests": {"e2":["e3","e4"]} }
  ```
  Uses `Engine.LegalDests`.

### 3.3 Join a game

- `POST .../games/:id/join` (no body) → `200` `GameStateResponse` (status
  `active`, clocks started). Errors: `404`, `403`, `409 game_full`,
  `409 already_joined`, `409 game_over`.

### 3.4 Submit a move

- `POST .../games/:id/move`
- Request:
  ```json
  { "orig": "e2", "dest": "e4", "promotion": "q|r|b|n|", "version": 0 }
  ```
- Response `200`:
  ```json
  { "accepted": true, "move": { "color":"w", "san":"e4", "uci":"e2e4", "moveNumber":1, "ply":1, "givesCheck":false }, "state": { "...GameStateResponse..." } }
  ```
- Errors: `400 illegal_move` / `400 promotion_required`, `403 not_a_player`,
  `409 not_your_turn`, `409 stale_move`, `409 game_over` (also used for flag
  fall: `"time expired"`).
- Service: `Service.Move`; uses `Engine.ApplyMove`, `ClockConfig.Apply/Flagged`.

### 3.5 Resign / draw

- `POST .../resign` → `200` state (result `1-0`/`0-1`, method `resignation`).
- `POST .../draw/offer` → `200` state with `drawOffer` set; broadcasts `draw.offer`.
- `POST .../draw/accept` → `200` state (result `1/2-1/2`, method
  `draw_agreement`); broadcasts `game.over`.
- `POST .../draw/decline` → `200` state (offer cleared); broadcasts `draw.decline`.

---

## 4. WebSocket protocol

`GET .../games/:id/ws` (browser native WebSocket; cookies sent automatically).
The handler authorizes via `Service.Connect` (active group member **and** a
player in the game), then upgrades. Origin is checked against
`cfg.FrontendOrigin`. Moves are submitted over HTTP; the socket is push-only.

Server → client (JSON, discriminated by `type`):

```jsonc
{"type":"game.state","state":{...GameStateResponse...}}        // on connect + resync
{"type":"game.move","move":{...},"state":{...}}                 // move accepted
{"type":"opponent.connected","userId":"..."}
{"type":"opponent.disconnected","userId":"..."}
{"type":"draw.offer","color":"w|b","userId":"..."}
{"type":"draw.decline"}
{"type":"game.over","result":"1-0|0-1|1/2-1/2","resultMethod":"...","state":{...}}
{"type":"pong"}
```

Client → server: `{"type":"ping"}` → `pong`.

Every state-changing event carries the full `state`, so **both players always
receive the same authoritative position**.

"Move rejected" is not a socket event: only the mover needs it, and it is
delivered as the HTTP 4xx response (the mover then re-syncs; the opponent is
unaffected).

---

## 5. Move submission and validation

`Player A` moves e2→e4:

1. Chessground fires `movable.events.after("e2","e4", meta)`.
2. Frontend `POST .../move` with `{orig:"e2",dest:"e4",promotion:"",version:<seen>}`.
3. Backend opens a transaction, checks the caller is an active group member
   (and room member if scoped), then loads the game `FOR UPDATE`.
4. Confirms `status == active`, the caller is a player, it is their turn, and
   `version` matches the stored ply count.
5. Reconstructs the rules game from the stored FEN and validates the move via
   `Engine.ApplyMove` (legal-move set). Promotion is required when a pawn
   reaches the last rank.
6. If the mover's clock has flagged, the game completes by `flag` and the move
   is rejected (`409 game_over`).
7. On success: update FEN, `version+1`, clock (remaining − elapsed + increment),
   move row (UCI/SAN/FEN-after/gives-check), and the outcome if the game ended.
8. Commit, then broadcast `game.move`/`game.state`/`game.over`.

An illegal move **never touches the database**.

Concurrency: the game row is locked with `SELECT … FOR UPDATE`, so simultaneous
moves serialise; the second sees a changed `version` (or a different side to
move) and is rejected. A unique `(game_id, ply)` index is a second safety net.

---

## 6. Database design (`migrations/0017_chess.sql`)

`chess_games`:

- `id`, `group_id` (FK→`study_groups`), `room_id` (optional, composite
  FK→`study_rooms(id,group_id)`), `white_player_id`/`black_player_id`
  (FK→`users`, `ON DELETE SET NULL`), `status`, `result`, `result_method`,
  `fen` (authoritative full FEN), `version` (ply count), `initial_seconds`,
  `increment_seconds`, `white_remaining_ms`, `black_remaining_ms`,
  `clock_started_at`, `draw_offer`, `created_by`, `created_at`, `started_at`,
  `completed_at`, `updated_at`.
- Constraints: status enum; result enum; draw offer enum; clock ranges and
  untimed-has-no-increment; `version >= 0`; distinct players; time ordering.
- Indexes: `(group_id, created_at DESC)`, `(room_id)` where not null.

`chess_moves`:

- `id bigint identity`, `game_id` (FK, CASCADE), `player_id` (FK), `color`,
  `move_number`, `ply`, `san`, `uci`, `fen_after`, `gives_check`, `created_at`.
- `UNIQUE (game_id, ply)`, index `(game_id, ply)`, UCI format check.

We deliberately do **not** add a `chess_players` table: a chess game has exactly
two players, so the two columns on `chess_games` are simpler and avoid
duplicating user records.

---

## 7. Chessground ↔ backend mapping (integration readiness)

When the backend returns a `GameStateResponse`, the frontend renders it with:

```ts
ground.set({
  fen: state.fen,             // full FEN; Chessground reads the piece part
  turnColor: state.turnColor, // "w" | "b"
  lastMove: state.lastMove,   // ["e2","e4"] | null
  check: state.check,         // "w" | "b" | null → pass colour to highlight
  movable: { dests: new Map(Object.entries(state.dests)), color: state.turnColor }
});
```

On a rejected move, re-`GET` (or use the last `game.state`) and `set(...)` again;
do not trust `getFen()`.

Example move request → response and WebSocket frames are in §3.4 and §4.

---

## 8. Implementation summary

- Added: `internal/chess/{engine,model,clock,dto,repository,service,websocket,handler}.go`.
- Added: `migrations/0017_chess.sql`.
- Modified: `cmd/api/main.go` (wiring + shutdown), `go.mod`/`go.sum`
  (`github.com/corentings/chess/v2 v2.6.1`).
- Tests: `engine_test.go` (rules: valid/illegal/checkmate/stalemate/castling/en
  passant/promotion/insufficient-material/dests), `clock_test.go`, and
  `service_integration_test.go` (wrong turn/player, stale move, simultaneous
  moves, unauthorized connect, full game — DB-gated on `TEST_DATABASE_URL`).

### Validation run

- `go build ./...` — passes.
- `go vet ./...` — passes.
- `go test ./internal/chess/` — passes (pure tests run; DB integration tests skip
  without `TEST_DATABASE_URL`).
