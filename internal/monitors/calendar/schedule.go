package calendarmonitor

import (
	"fmt"
	"math"
	"time"
)

// Wall-clock hours that shape the reminder schedule, in the monitor's timezone.
const (
	nightBeforeHour = 20 // reminder the evening before the event day
	morningHour     = 8  // morning-of reminder
	noonHour        = 12 // noon-of reminder; also where "morning events" end
	eveningHour     = 17 // events starting at or after this are "evening events"
)

// Slot is one scheduled reminder for an event.
type Slot struct {
	Name string    // stable identifier, part of the dedup key
	At   time.Time // when the reminder becomes due
}

// ReminderSlots returns the reminders for an event starting at start, ordered
// by time. Every event gets one the night before and 1h and 30m before.
// Afternoon events (12:00–17:00) add a morning reminder; evening events
// (17:00+) add morning and noon reminders. Slots at or after start are dropped.
func ReminderSlots(start time.Time, loc *time.Location) []Slot {
	local := start.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	at := func(d time.Time, hour int) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, loc)
	}

	slots := []Slot{{Name: "night-before", At: at(day.AddDate(0, 0, -1), nightBeforeHour)}}
	if local.Hour() >= noonHour {
		slots = append(slots, Slot{Name: "morning", At: at(day, morningHour)})
	}
	if local.Hour() >= eveningHour {
		slots = append(slots, Slot{Name: "noon", At: at(day, noonHour)})
	}
	slots = append(slots,
		Slot{Name: "1h", At: start.Add(-time.Hour)},
		Slot{Name: "30m", At: start.Add(-30 * time.Minute)},
	)

	var out []Slot
	for _, s := range slots {
		if s.At.Before(start) {
			out = append(out, s)
		}
	}
	return out
}

// formatWhen describes when an event starts relative to now, e.g.
// "today at 17:30 — in 2 hours 30 minutes" or "tomorrow at 09:00 — in 13 hours".
func formatWhen(start, now time.Time, loc *time.Location) string {
	s, n := start.In(loc), now.In(loc)
	sDay := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, loc)
	nDay := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)

	var day string
	switch days := int(math.Round(sDay.Sub(nDay).Hours() / 24)); days {
	case 0:
		day = "today"
	case 1:
		day = "tomorrow"
	default:
		day = "on " + s.Weekday().String()
	}
	return fmt.Sprintf("%s at %s — in %s", day, s.Format("15:04"), formatUntil(start.Sub(now)))
}

// formatUntil renders a time-until in hours and minutes, rounded to 5 minutes
// (or to the hour beyond 6 hours, where minutes are noise).
func formatUntil(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	if d >= 6*time.Hour {
		d = d.Round(time.Hour)
	} else if d >= 5*time.Minute {
		d = d.Round(5 * time.Minute)
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h == 0:
		return fmt.Sprintf("%d minute%s", m, pluralS(m))
	case m == 0:
		return fmt.Sprintf("%d hour%s", h, pluralS(h))
	default:
		return fmt.Sprintf("%d hour%s %d minute%s", h, pluralS(h), m, pluralS(m))
	}
}
