// internal/scheduling/recurrence.go
package scheduling

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// rrule is a minimal RFC 5545 subset. The DB CHECK constraint accepts exactly
// the shape: FREQ=(DAILY|WEEKLY|MONTHLY)(;[A-Z]+=[A-Z0-9,+-]+)*
// We support:
//   FREQ=DAILY   [;INTERVAL=N]
//   FREQ=WEEKLY  [;INTERVAL=N][;BYDAY=MO,TU,...]
//   FREQ=MONTHLY [;INTERVAL=N][;BYMONTHDAY=1,15,-1]
type rrule struct {
	Freq       string
	Interval   int
	ByDay      []time.Weekday
	ByMonthDay []int
}

var weekdayMap = map[string]time.Weekday{
	"MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday, "SU": time.Sunday,
}

func parseRRule(s string) (*rrule, error) {
	r := &rrule{Interval: 1}
	for _, part := range strings.Split(s, ";") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("bad rrule part %q", part)
		}
		key, val := kv[0], kv[1]
		switch key {
		case "FREQ":
			r.Freq = val
		case "INTERVAL":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("bad INTERVAL")
			}
			r.Interval = n
		case "BYDAY":
			for _, d := range strings.Split(val, ",") {
				wd, ok := weekdayMap[d]
				if !ok {
					return nil, fmt.Errorf("bad BYDAY %q", d)
				}
				r.ByDay = append(r.ByDay, wd)
			}
		case "BYMONTHDAY":
			for _, d := range strings.Split(val, ",") {
				n, err := strconv.Atoi(d)
				if err != nil || n == 0 || n < -31 || n > 31 {
					return nil, fmt.Errorf("bad BYMONTHDAY %q", d)
				}
				r.ByMonthDay = append(r.ByMonthDay, n)
			}
		}
	}
	switch r.Freq {
	case "DAILY", "WEEKLY", "MONTHLY":
	default:
		return nil, fmt.Errorf("unsupported FREQ %q", r.Freq)
	}
	if r.Freq == "WEEKLY" && len(r.ByDay) == 0 {
		return nil, fmt.Errorf("WEEKLY requires BYDAY")
	}
	if r.Freq == "MONTHLY" && len(r.ByMonthDay) == 0 {
		return nil, fmt.Errorf("MONTHLY requires BYMONTHDAY")
	}
	return r, nil
}

// occurrencesBetween produces absolute start times in `window` for a series.
//
// We anchor on `series.StartsOn` in the series' timezone at LocalStartTime,
// then step forward by the rule until we exceed window.End. This is the
// canonical expansion; the DB's UNIQUE (series_id, original_starts_at)
// constraint is what makes repeated runs safe.
func occurrencesBetween(series *Series, windowStart, windowEnd time.Time) ([]time.Time, error) {
	rule, err := parseRRule(series.RecurrenceRule)
	if err != nil {
		return nil, err
	}

	loc, err := time.LoadLocation(series.Timezone)
	if err != nil {
		return nil, fmt.Errorf("load timezone: %w", err)
	}

	// Parse LocalStartTime ("HH:MM:SS") into hour/min/sec.
	var hh, mm, ss int
	if _, err := fmt.Sscanf(series.LocalStartTime, "%d:%d:%d", &hh, &mm, &ss); err != nil {
		return nil, fmt.Errorf("parse local_start_time: %w", err)
	}

	// Anchor: first moment of StartsOn in the series timezone.
	anchor := time.Date(
		series.StartsOn.Year(), series.StartsOn.Month(), series.StartsOn.Day(),
		hh, mm, ss, 0, loc)

	// Hard stop: ends_on is a date; treat the entire day as inclusive.
	var hardEnd time.Time
	if series.EndsOn != nil {
		hardEnd = time.Date(series.EndsOn.Year(), series.EndsOn.Month(), series.EndsOn.Day(),
			23, 59, 59, 0, loc)
	}

	// Fast-forward: for a series that started long ago, advance the anchor
	// close to windowStart so we don't iterate years of occurrences.
	if anchor.Before(windowStart) {
		switch rule.Freq {
		case "DAILY":
			// days between anchor and windowStart
			days := int(windowStart.In(loc).Sub(anchor).Hours() / 24)
			steps := days / rule.Interval
			anchor = anchor.AddDate(0, 0, steps*rule.Interval)
		case "WEEKLY":
			weeks := int(windowStart.In(loc).Sub(anchor).Hours() / (24 * 7))
			steps := weeks / rule.Interval
			anchor = anchor.AddDate(0, 0, steps*7*rule.Interval)
		case "MONTHLY":
			months := (windowStart.In(loc).Year()-anchor.Year())*12 + int(windowStart.In(loc).Month()-anchor.Month())
			steps := months / rule.Interval
			anchor = anchor.AddDate(0, steps*rule.Interval, 0)
		}
	}

	var out []time.Time
	add := func(t time.Time) {
		// Inclusive both ends: [windowStart, windowEnd)
		if t.Before(windowStart) {
			return
		}
		if !t.Before(windowEnd) {
			return
		}
		if !hardEnd.IsZero() && t.After(hardEnd) {
			return
		}
		out = append(out, t.UTC())
	}

	// Bounded loop guard: even with week-long windows we should never see more
	// than a few hundred iterations.
	const maxIterations = 5000
	iter := 0
	iterations := func() bool { iter++; return iter < maxIterations }

	switch rule.Freq {
	case "DAILY":
		for t := anchor; iterations(); t = t.AddDate(0, 0, rule.Interval) {
			if !t.Before(windowEnd) {
				break
			}
			add(t)
		}

	case "WEEKLY":
		// Iterate weeks; within each active week, emit one occurrence per BYDAY.
		weekStart := anchor.AddDate(0, 0, -int(anchor.Weekday()))
		for w := weekStart; iterations(); w = w.AddDate(0, 0, 7*rule.Interval) {
			emitted := false
			for _, wd := range rule.ByDay {
				day := w.AddDate(0, 0, int(wd))
				occ := time.Date(day.Year(), day.Month(), day.Day(), hh, mm, ss, 0, loc)
				if occ.Before(anchor) {
					continue // before series start
				}
				if !occ.Before(windowEnd) {
					continue
				}
				add(occ)
				emitted = true
			}
			if !emitted && w.After(windowEnd) {
				break
			}
		}

	case "MONTHLY":
		for m := anchor; iterations(); m = m.AddDate(0, rule.Interval, 0) {
			if !m.Before(windowEnd) {
				break
			}
			for _, d := range rule.ByMonthDay {
				var occ time.Time
				if d > 0 {
					occ = time.Date(m.Year(), m.Month(), d, hh, mm, ss, 0, loc)
					// Rollover guard: if d > days in month, pg-style
					// would skip; skip if month changed.
					if occ.Month() != m.Month() {
						continue
					}
				} else {
					// Negative: -1 = last day of month.
					last := time.Date(m.Year(), m.Month()+1, 0, hh, mm, ss, 0, loc)
					occ = last.AddDate(0, 0, d+1)
				}
				if occ.Before(anchor) {
					continue
				}
				add(occ)
			}
		}
	}

	return out, nil
}