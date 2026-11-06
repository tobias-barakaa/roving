// internal/chess/clock_test.go
package chess

import (
	"testing"
	"time"
)

func TestClockUntimed(t *testing.T) {
	c := ClockConfig{InitialSeconds: 0, IncrementSeconds: 0}
	if c.Timed() {
		t.Fatal("0 initial seconds should be untimed")
	}
	if c.InitialMs() != nil {
		t.Fatal("untimed clock should have no initial millis")
	}
	if c.Flagged(0, time.Hour) {
		t.Fatal("untimed clock can never flag")
	}
}

func TestClockApplyIncrement(t *testing.T) {
	c := ClockConfig{InitialSeconds: 300, IncrementSeconds: 3}
	// 300s - 10s elapsed + 3s increment = 293s.
	got := c.Apply(300_000, 10*time.Second)
	if got != 293_000 {
		t.Fatalf("Apply = %d, want 293000", got)
	}
}

func TestClockApplyClampsAtZero(t *testing.T) {
	c := ClockConfig{InitialSeconds: 10, IncrementSeconds: 0}
	if got := c.Apply(5_000, 30*time.Second); got != 0 {
		t.Fatalf("Apply = %d, want 0", got)
	}
}

func TestClockFlagged(t *testing.T) {
	c := ClockConfig{InitialSeconds: 60, IncrementSeconds: 0}
	if !c.Flagged(6_000, 6*time.Second) {
		t.Fatal("exactly zero remaining should flag")
	}
	if !c.Flagged(5_000, 6*time.Second) {
		t.Fatal("negative remaining should flag")
	}
	if c.Flagged(7_000, 6*time.Second) {
		t.Fatal("positive remaining should not flag")
	}
}
