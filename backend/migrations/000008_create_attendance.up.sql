-- One row per user per occurrence. An RSVP is a plan; a check-in is evidence.
-- Rows are history: a session with attendance cannot be hard-deleted; cancel it.
-- Users deleting their account removes their own attendance rows.

CREATE TABLE session_attendance (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id        uuid NOT NULL REFERENCES study_sessions(id),
    user_id           uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    rsvp_status       text NOT NULL DEFAULT 'pending',
    attendance_status text NOT NULL DEFAULT 'unknown',
    responded_at      timestamptz,
    checked_in_at     timestamptz,
    checked_out_at    timestamptz,
    absence_reason    text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT session_attendance_session_user_key UNIQUE (session_id, user_id),
    CONSTRAINT session_attendance_rsvp CHECK (rsvp_status IN ('pending', 'going', 'maybe', 'declined')),
    CONSTRAINT session_attendance_status CHECK (attendance_status IN ('unknown', 'present', 'late', 'absent', 'excused')),
    CONSTRAINT session_attendance_responded CHECK (rsvp_status = 'pending' OR responded_at IS NOT NULL),
    CONSTRAINT session_attendance_present_has_checkin
        CHECK (attendance_status NOT IN ('present', 'late') OR checked_in_at IS NOT NULL),
    CONSTRAINT session_attendance_checkout_after_checkin
        CHECK (checked_out_at IS NULL OR (checked_in_at IS NOT NULL AND checked_out_at >= checked_in_at)),
    CONSTRAINT session_attendance_reason_scope
        CHECK (absence_reason IS NULL OR attendance_status IN ('absent', 'excused')),
    CONSTRAINT session_attendance_reason_length
        CHECK (absence_reason IS NULL OR char_length(absence_reason) <= 500)
);
CREATE INDEX session_attendance_user_idx ON session_attendance (user_id, created_at DESC);
-- Rosters ("who is going?") and the absence-marking job.
CREATE INDEX session_attendance_session_rsvp_idx ON session_attendance (session_id, rsvp_status);
CREATE TRIGGER session_attendance_set_updated_at
    BEFORE UPDATE ON session_attendance
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();