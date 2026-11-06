// internal/chess/clock.go
package chess

import "time"

// ClockConfig describes a Fischer time control. InitialSeconds == 0 means the
// game is untimed and IncrementSeconds must also be 0.
type ClockConfig struct {
	InitialSeconds   int
	IncrementSeconds int
}

// Timed reports whether the game uses a clock.
func (c ClockConfig) Timed() bool { return c.InitialSeconds > 0 }

// InitialMs returns each player's starting clock in milliseconds, or nil when
// the game is untimed.
func (c ClockConfig) InitialMs() *int64 {
	if !c.Timed() {
		return nil
	}
	v := int64(c.InitialSeconds) * 1000
	return &v
}

// Apply returns the mover's remaining milliseconds after spending elapsed time
// and receiving the per-move increment. The result never drops below zero.
func (c ClockConfig) Apply(remainingMs int64, elapsed time.Duration) int64 {
	if !c.Timed() {
		return remainingMs
	}
	remaining := remainingMs - elapsed.Milliseconds()
	if remaining < 0 {
		remaining = 0
	}
	remaining += int64(c.IncrementSeconds) * 1000
	return remaining
}

// Flagged reports whether the mover ran out of time.
func (c ClockConfig) Flagged(remainingMs int64, elapsed time.Duration) bool {
	if !c.Timed() {
		return false
	}
	return remainingMs-elapsed.Milliseconds() <= 0
}
