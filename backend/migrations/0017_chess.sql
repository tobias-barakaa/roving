-- Chess games between two Rovingr users, played during study breaks.
--
-- A game belongs to a group and optionally to one of its rooms. Exactly two
-- users play (white_player_id / black_player_id); there is deliberately no
-- separate chess_players table because the two slots are fixed by the rules.
-- The authoritative position is the full FEN in "fen" (not a Chessground
-- getFen(), which omits turn/castling/en passant/halfmove clocks).
--
-- "version" is the ply count and the optimistic-concurrency guard: a move
-- request quotes the version it saw, and the service compares it under a row
-- lock so two simultaneous moves can never diverge.

CREATE TABLE chess_games (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id           uuid NOT NULL REFERENCES study_groups(id) ON DELETE CASCADE,
    room_id            uuid,
    white_player_id    uuid REFERENCES users(id) ON DELETE SET NULL,
    black_player_id    uuid REFERENCES users(id) ON DELETE SET NULL,
    status             text NOT NULL DEFAULT 'pending',
    result             text,
    result_method      text,
    fen                text NOT NULL,
    version            integer NOT NULL DEFAULT 0,
    initial_seconds    integer NOT NULL DEFAULT 0,  -- 0 = untimed
    increment_seconds  integer NOT NULL DEFAULT 0,
    white_remaining_ms bigint,
    black_remaining_ms bigint,
    clock_started_at   timestamptz,
    draw_offer         text,
    created_by         uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at         timestamptz NOT NULL DEFAULT now(),
    started_at         timestamptz,
    completed_at       timestamptz,
    updated_at         timestamptz NOT NULL DEFAULT now(),
    -- A room-scoped game can only belong to the room's own group.
    CONSTRAINT chess_games_room_fk
        FOREIGN KEY (room_id, group_id) REFERENCES study_rooms (id, group_id) ON DELETE CASCADE,
    CONSTRAINT chess_games_status CHECK (status IN ('pending', 'active', 'completed')),
    CONSTRAINT chess_games_result CHECK (
        result IS NULL OR result IN ('1-0', '0-1', '1/2-1/2')),
    CONSTRAINT chess_games_draw_offer CHECK (draw_offer IS NULL OR draw_offer IN ('w', 'b')),
    CONSTRAINT chess_games_clock_range CHECK (
        initial_seconds BETWEEN 0 AND 604800 AND increment_seconds BETWEEN 0 AND 3600),
    CONSTRAINT chess_games_untimed_no_increment CHECK (initial_seconds > 0 OR increment_seconds = 0),
    CONSTRAINT chess_games_version_nonnegative CHECK (version >= 0),
    CONSTRAINT chess_games_players_distinct CHECK (
        white_player_id IS NULL OR black_player_id IS NULL OR white_player_id <> black_player_id),
    CONSTRAINT chess_games_time_order CHECK (
        completed_at IS NULL OR started_at IS NULL OR completed_at >= started_at)
);
CREATE INDEX chess_games_group_idx ON chess_games (group_id, created_at DESC);
CREATE INDEX chess_games_room_idx ON chess_games (room_id) WHERE room_id IS NOT NULL;
CREATE TRIGGER chess_games_set_updated_at
    BEFORE UPDATE ON chess_games
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE chess_moves (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    game_id     uuid NOT NULL REFERENCES chess_games(id) ON DELETE CASCADE,
    player_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    color       text NOT NULL CHECK (color IN ('w', 'b')),
    move_number integer NOT NULL,
    ply         integer NOT NULL,
    san         text NOT NULL,
    uci         text NOT NULL,
    fen_after   text NOT NULL,
    gives_check boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    -- One authoritative half-move per ply; a second insert for the same ply is
    -- a concurrency bug and is rejected even before the row lock catches it.
    CONSTRAINT chess_moves_game_ply_key UNIQUE (game_id, ply),
    CONSTRAINT chess_moves_uci_format CHECK (uci ~ '^[a-h][1-8][a-h][1-8][qrbn]?$')
);
CREATE INDEX chess_moves_game_idx ON chess_moves (game_id, ply);

-- ---- down ----
-- DROP TABLE IF EXISTS chess_moves;
-- DROP TABLE IF EXISTS chess_games;
