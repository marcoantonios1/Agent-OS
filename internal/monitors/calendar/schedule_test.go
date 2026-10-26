package calendarmonitor_test

import (
	"testing"
	"time"

	calendarmonitor "github.com/marcoantonios1/Agent-OS/internal/monitors/calendar"
)

func TestReminderSlots(t *testing.T) {
	day := func(h, m int) time.Time { return time.Date(2026, 10, 28, h, m, 0, 0, time.UTC) }
	cases := []struct {
		name  string
		start time.Time
		want  []string
	}{
		{"morning", day(9, 30), []string{"night-before 10-27 20:00", "1h 10-28 08:30", "30m 10-28 09:00"}},
		{"noon", day(13, 0), []string{"night-before 10-27 20:00", "morning 10-28 08:00", "1h 10-28 12:00", "30m 10-28 12:30"}},
		{"evening", day(19, 0), []string{"night-before 10-27 20:00", "morning 10-28 08:00", "noon 10-28 12:00", "1h 10-28 18:00", "30m 10-28 18:30"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			slots := calendarmonitor.ReminderSlots(tc.start, time.UTC)
			var got []string
			for _, s := range slots {
				got = append(got, s.Name+" "+s.At.Format("01-02 15:04"))
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("slot %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestReminderSlots_UsesLocation(t *testing.T) {
	stockholm, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	// 14:00 UTC is 15:00 in Stockholm (CET) — an afternoon event there.
	start := time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)
	slots := calendarmonitor.ReminderSlots(start, stockholm)
	if slots[1].Name != "morning" || slots[1].At.In(stockholm).Hour() != 8 {
		t.Errorf("expected morning slot at 08:00 Stockholm time, got %s at %s", slots[1].Name, slots[1].At.In(stockholm))
	}
}
