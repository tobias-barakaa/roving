
package scheduling

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// rrule is a minimal RFC 5545 subset.
//
// Supported:
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
	"MO": time.Monday,
	"TU": time.Tuesday,
	"WE": time.Wednesday,
	"TH": time.Thursday,
	"FR": time.Friday,
	"SA": time.Saturday,
	"SU": time.Sunday,
}

func parseRRule(s string) (*rrule, error) {
	r := &rrule{Interval: 1}
	seen := make(map[string]bool)

	for _, part := range strings.Split(s, ";") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 || kv[0] == "" || kv[1] == "" {
			return nil, fmt.Errorf("bad rrule part %q", part)
		}

		key, val := kv[0], kv[1]

		if seen[key] {
			return nil, fmt.Errorf("duplicate rrule field %q", key)
		}
		seen[key] = true

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
			seenDays := make(map[time.Weekday]bool)

			for _, d := range strings.Split(val, ",") {
				wd, ok := weekdayMap[d]
				if !ok {
					return nil, fmt.Errorf("bad BYDAY %q", d)
				}

				if seenDays[wd] {
					return nil, fmt.Errorf("duplicate BYDAY %q", d)
				}

				seenDays[wd] = true
				r.ByDay = append(r.ByDay, wd)
			}

		case "BYMONTHDAY":
			seenDays := make(map[int]bool)

			for _, d := range strings.Split(val, ",") {
				n, err := strconv.Atoi(d)
				if err != nil || n == 0 || n < -31 || n > 31 {
					return nil, fmt.Errorf("bad BYMONTHDAY %q", d)
				}

				if seenDays[n] {
					return nil, fmt.Errorf("duplicate BYMONTHDAY %q", d)
				}

				seenDays[n] = true
				r.ByMonthDay = append(r.ByMonthDay, n)
			}

		default:
			return nil, fmt.Errorf("unsupported rrule field %q", key)
		}
	}

	switch r.Freq {
	case "DAILY":
		if len(r.ByDay) > 0 || len(r.ByMonthDay) > 0 {
			return nil, fmt.Errorf("DAILY does not support BYDAY or BYMONTHDAY")
		}

	case "WEEKLY":
		if len(r.ByDay) == 0 {
			return nil, fmt.Errorf("WEEKLY requires BYDAY")
		}
		if len(r.ByMonthDay) > 0 {
			return nil, fmt.Errorf("WEEKLY does not support BYMONTHDAY")
		}

	case "MONTHLY":
		if len(r.ByMonthDay) == 0 {
			return nil, fmt.Errorf("MONTHLY requires BYMONTHDAY")
		}
		if len(r.ByDay) > 0 {
			return nil, fmt.Errorf("MONTHLY does not support BYDAY")
		}

	default:
		return nil, fmt.Errorf("unsupported FREQ %q", r.Freq)
	}

	return r, nil
}

// fastForward advances the recurrence close to windowStart.
//
// Calendar-date arithmetic is used instead of elapsed hours so DST
// transitions cannot shift the recurrence interval.
//
// For MONTHLY, it returns the first day of the aligned month.
// The original series anchor is preserved separately by the caller.
func fastForward(
	anchor, windowStart time.Time,
	rule *rrule,
	loc *time.Location,
) time.Time {
	if !anchor.Before(windowStart) {
		if rule.Freq == "MONTHLY" {
			a := anchor.In(loc)
			return time.Date(a.Year(), a.Month(), 1, 0, 0, 0, 0, loc)
		}
		return anchor
	}

	a := anchor.In(loc)
	ws := windowStart.In(loc)

	// Compare calendar dates, not elapsed durations.
	aDate := time.Date(
		a.Year(), a.Month(), a.Day(),
		0, 0, 0, 0, time.UTC,
	)

	wsDate := time.Date(
		ws.Year(), ws.Month(), ws.Day(),
		0, 0, 0, 0, time.UTC,
	)

	switch rule.Freq {
	case "DAILY":
		days := int(wsDate.Sub(aDate).Hours() / 24)
		steps := days / rule.Interval

		return anchor.AddDate(0, 0, steps*rule.Interval)

	case "WEEKLY":
		// Preserve the original Sunday-based recurrence week.
		anchorWeek := aDate.AddDate(
			0, 0, -int(a.Weekday()),
		)

		windowWeek := wsDate.AddDate(
			0, 0, -int(ws.Weekday()),
		)

		weeks := int(windowWeek.Sub(anchorWeek).Hours() / (24 * 7))
		steps := weeks / rule.Interval

		return anchor.AddDate(0, 0, steps*7*rule.Interval)

	case "MONTHLY":
		months := (ws.Year()-a.Year())*12 +
			int(ws.Month()-a.Month())

		steps := months / rule.Interval

		// Always return the first day of the aligned month.
		// Never advance a date such as January 31 directly.
		target := aDate.AddDate(0, steps*rule.Interval, 0)

		return time.Date(
			target.Year(),
			target.Month(),
			1,
			0, 0, 0, 0,
			loc,
		)
	}

	return anchor
}

// occurrencesBetween produces absolute start times inside the requested window.
//
// The recurrence is always calculated from the original series definition.
// Generated occurrences are returned in UTC.
//
// Window boundaries:
//   - windowStart: inclusive
//   - windowEnd: exclusive
//
// EndsOn is an inclusive calendar date in the series timezone.
func occurrencesBetween(
	series *Series,
	windowStart, windowEnd time.Time,
) ([]time.Time, error) {
	rule, err := parseRRule(series.RecurrenceRule)
	if err != nil {
		return nil, err
	}

	loc, err := time.LoadLocation(series.Timezone)
	if err != nil {
		return nil, fmt.Errorf("load timezone: %w", err)
	}

	// Parse LocalStartTime ("HH:MM:SS").
	var hh, mm, ss int

	if _, err := fmt.Sscanf(
		series.LocalStartTime,
		"%d:%d:%d",
		&hh, &mm, &ss,
	); err != nil {
		return nil, fmt.Errorf("parse local_start_time: %w", err)
	}

	if hh < 0 || hh > 23 || mm < 0 || mm > 59 || ss < 0 || ss > 59 {
		return nil, fmt.Errorf("invalid local_start_time %q", series.LocalStartTime)
	}

	if !windowStart.Before(windowEnd) {
		return nil, nil
	}

	// Original series anchor.
	// Do not overwrite this when fast-forwarding.
	seriesStart := time.Date(
		series.StartsOn.Year(),
		series.StartsOn.Month(),
		series.StartsOn.Day(),
		hh, mm, ss,
		0,
		loc,
	)

	// Inclusive hard stop: the entire EndsOn date is valid.
	var hardEnd time.Time

	if series.EndsOn != nil {
		hardEnd = time.Date(
			series.EndsOn.Year(),
			series.EndsOn.Month(),
			series.EndsOn.Day(),
			23, 59, 59,
			0,
			loc,
		)
	}

	// Move close to the requested window without losing the
	// original recurrence alignment.
	cursor := fastForward(
		seriesStart,
		windowStart,
		rule,
		loc,
	)

	var out []time.Time

	// Include windowStart, exclude windowEnd.
	add := func(t time.Time) {
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

	// Prevent unexpectedly large recurrence expansions.
	const maxIterations = 5000

	iter := 0

	iterations := func() bool {
		if iter >= maxIterations {
			return false
		}

		iter++
		return true
	}

	switch rule.Freq {

	case "DAILY":
		for t := cursor; iterations(); t = t.AddDate(0, 0, rule.Interval) {
			if !t.Before(windowEnd) {
				break
			}

			add(t)
		}

	case "WEEKLY":
		// Sunday-based week containing the fast-forwarded anchor.
		// Since fastForward advances by complete recurrence weeks,
		// this retains the original weekly alignment.
		weekStart := cursor.AddDate(
			0, 0, -int(cursor.Weekday()),
		)

		for w := weekStart; iterations(); w = w.AddDate(
			0, 0, 7*rule.Interval,
		) {
			if !w.Before(windowEnd) {
				break
			}

			for _, wd := range rule.ByDay {
				day := w.AddDate(0, 0, int(wd))

				occ := time.Date(
					day.Year(),
					day.Month(),
					day.Day(),
					hh, mm, ss,
					0,
					loc,
				)

				// Never generate an occurrence before the series began.
				if occ.Before(seriesStart) {
					continue
				}

				add(occ)
			}
		}

	case "MONTHLY":
		// cursor is always the first day of an aligned month.
		// This avoids AddDate month normalization problems with
		// dates such as January 29, 30, or 31.
		for m := cursor; iterations(); m = m.AddDate(
			0, rule.Interval, 0,
		) {
			if !m.Before(windowEnd) {
				break
			}

			daysInMonth := time.Date(
				m.Year(),
				m.Month()+1,
				0,
				0, 0, 0, 0,
				loc,
			).Day()

			for _, d := range rule.ByMonthDay {
				day := d

				if d < 0 {
					// -1 = last day
					// -2 = second-to-last day
					day = daysInMonth + d + 1
				}

				// Skip dates that do not exist in this month.
				if day < 1 || day > daysInMonth {
					continue
				}

				occ := time.Date(
					m.Year(),
					m.Month(),
					day,
					hh, mm, ss,
					0,
					loc,
				)

				if occ.Before(seriesStart) {
					continue
				}

				add(occ)
			}
		}
	}

	// BYDAY and BYMONTHDAY may be supplied in any order.
	// Return occurrences chronologically.
	sort.Slice(out, func(i, j int) bool {
		return out[i].Before(out[j])
	})

	return out, nil
}
