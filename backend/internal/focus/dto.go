// internal/focus/dto.go
package focus

import "time"

// --- requests ---

type StartCycleRequest struct {
	// Optional context. If both provided, they must agree on group_id.
	StudySessionID *string `json:"studySessionId" binding:"omitempty,uuid"`
	RoomID         *string `json:"roomId"         binding:"omitempty,uuid"`

	// Plan parameters; defaults applied server-side if omitted.
	PlannedFocusSeconds *int `json:"plannedFocusSeconds" binding:"omitempty,min=60,max=14400"`
	PlannedBreakSeconds *int `json:"plannedBreakSeconds" binding:"omitempty,min=0,max=3600"`
	PlannedRounds       *int `json:"plannedRounds"       binding:"omitempty,min=1,max=20"`
}

// StartIntervalRequest starts the first (or next) interval inside the
// running cycle. If no cycle is running, it auto-creates a default cycle
// (25/5×4) and starts the first interval — this makes "just start a timer"
// the common case without an extra round-trip.
type StartIntervalRequest struct {
	Kind string `json:"kind" binding:"required,oneof=focus short_break long_break"`
	// Optional override of planned_seconds for this one interval.
	// If omitted, derived from cycle plan (focus_seconds or break_seconds).
	PlannedSeconds *int `json:"plannedSeconds" binding:"omitempty,min=1,max=14400"`
}

// CreateIntervalRequest is used by the auto-create path; not exposed directly.
type createCycleAndInterval struct {
	ctx     ContextInput
	focus   int
	breakS  int
	rounds  int
	kind    string
	planned int
}

// EndIntervalRequest is used to finish or interrupt the running interval.
// The server uses now() for `ended_at`; we don't trust client time.
type EndIntervalRequest struct {
	Interrupted bool `json:"interrupted"`
}

// EndCycleRequest finishes the running cycle; any running interval is
// interrupted as part of the same transaction.
type EndCycleRequest struct {
	// If true, mark cycle as 'completed' rather than 'cancelled'.
	Complete bool `json:"complete"`
}

type ListCyclesQuery struct {
	Status string `form:"status" binding:"omitempty,oneof=planned running completed cancelled"`
	From   string `form:"from"   binding:"omitempty"` // RFC3339
	To     string `form:"to"     binding:"omitempty"`
	Limit  int    `form:"limit"  binding:"omitempty,min=1,max=200"`
	Offset int    `form:"offset" binding:"min=0"`
}

type StatsQuery struct {
	From string `form:"from" binding:"required"` // RFC3339
	To   string `form:"to"   binding:"required"`
}

// --- responses ---

type CycleResponse struct {
	ID                  string     `json:"id"`
	StudySessionID      *string    `json:"studySessionId,omitempty"`
	RoomID              *string    `json:"roomId,omitempty"`
	Status              string     `json:"status"`
	PlannedFocusSeconds int        `json:"plannedFocusSeconds"`
	PlannedBreakSeconds int        `json:"plannedBreakSeconds"`
	PlannedRounds       int        `json:"plannedRounds"`
	StartedAt           *time.Time `json:"startedAt,omitempty"`
	EndedAt             *time.Time `json:"endedAt,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

type IntervalResponse struct {
	ID             string     `json:"id"`
	CycleID        *string    `json:"cycleId,omitempty"`
	StudySessionID *string    `json:"studySessionId,omitempty"`
	RoomID         *string    `json:"roomId,omitempty"`
	SequenceNo     *int       `json:"sequenceNo,omitempty"`
	Kind           string     `json:"kind"`
	Status         string     `json:"status"`
	PlannedSeconds int        `json:"plannedSeconds"`
	ActualSeconds  *int       `json:"actualSeconds,omitempty"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	EndedAt        *time.Time `json:"endedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type CycleDetailResponse struct {
	CycleResponse
	Intervals            []IntervalResponse `json:"intervals"`
	TotalFocusSeconds    int                `json:"totalFocusSeconds"`
	TotalBreakSeconds    int                `json:"totalBreakSeconds"`
	CompletedFocusRounds int                `json:"completedFocusRounds"`
}

type StatsResponse struct {
	From              time.Time `json:"from"`
	To                time.Time `json:"to"`
	TotalFocusSeconds int       `json:"totalFocusSeconds"`
	TotalBreakSeconds int       `json:"totalBreakSeconds"`
	CycleCount        int       `json:"cycleCount"`
	SessionCount      int       `json:"sessionCount"`
}

// --- mapping ---

func toCycleResponse(c *Cycle) CycleResponse {
	return CycleResponse{
		ID:                  c.ID,
		StudySessionID:      c.StudySessionID,
		RoomID:              c.RoomID,
		Status:              c.Status,
		PlannedFocusSeconds: c.PlannedFocusSeconds,
		PlannedBreakSeconds: c.PlannedBreakSeconds,
		PlannedRounds:       c.PlannedRounds,
		StartedAt:           c.StartedAt,
		EndedAt:             c.EndedAt,
		CreatedAt:           c.CreatedAt,
		UpdatedAt:           c.UpdatedAt,
	}
}

func toIntervalResponse(i *Interval) IntervalResponse {
	return IntervalResponse{
		ID:             i.ID,
		CycleID:        i.CycleID,
		StudySessionID: i.StudySessionID,
		RoomID:         i.RoomID,
		SequenceNo:     i.SequenceNo,
		Kind:           i.Kind,
		Status:         i.Status,
		PlannedSeconds: i.PlannedSeconds,
		ActualSeconds:  i.ActualSeconds,
		StartedAt:      i.StartedAt,
		EndedAt:        i.EndedAt,
		CreatedAt:      i.CreatedAt,
		UpdatedAt:      i.UpdatedAt,
	}
}

func toCycleDetailResponse(d *CycleDetail) CycleDetailResponse {
	ivs := make([]IntervalResponse, len(d.Intervals))
	for i := range d.Intervals {
		ivs[i] = toIntervalResponse(&d.Intervals[i])
	}
	return CycleDetailResponse{
		CycleResponse:        toCycleResponse(&d.Cycle),
		Intervals:            ivs,
		TotalFocusSeconds:    d.TotalFocusSeconds,
		TotalBreakSeconds:    d.TotalBreakSeconds,
		CompletedFocusRounds: d.CompletedFocusRounds,
	}
}