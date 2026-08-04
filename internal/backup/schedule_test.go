package backup

import (
	"testing"
	"time"
)

func TestScheduleNextDailyUsesTimezone(t *testing.T) {
	schedule := Schedule{Kind: ScheduleDaily, TimeOfDay: "02:30", Timezone: "Asia/Kolkata"}
	after := time.Date(2026, 8, 4, 20, 0, 0, 0, time.UTC)
	next, ok, err := schedule.Next(after)
	if err != nil || !ok {
		t.Fatalf("Next() = %v, %v, %v", next, ok, err)
	}
	want := time.Date(2026, 8, 4, 21, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestScheduleNextWeekly(t *testing.T) {
	schedule := Schedule{Kind: ScheduleWeekly, TimeOfDay: "09:00", Timezone: "UTC", Weekdays: []int{int(time.Monday), int(time.Friday)}}
	after := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC) // Tuesday.
	next, ok, err := schedule.Next(after)
	if err != nil || !ok {
		t.Fatalf("Next() = %v, %v, %v", next, ok, err)
	}
	want := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestScheduleRejectsShortPollingInterval(t *testing.T) {
	err := (Schedule{Kind: ScheduleInterval, EveryMinutes: 1}).Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestScheduleAdvancePastPreservesIntervalCadence(t *testing.T) {
	schedule := Schedule{Kind: ScheduleInterval, EveryMinutes: 15}
	scheduled := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 4, 8, 47, 0, 0, time.UTC)

	next, ok, err := schedule.AdvancePast(scheduled, now)
	if err != nil || !ok {
		t.Fatalf("AdvancePast() = %v, %v, %v", next, ok, err)
	}
	want := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestScheduleAdvancePastDailyUsesWallClock(t *testing.T) {
	schedule := Schedule{Kind: ScheduleDaily, TimeOfDay: "02:30", Timezone: "Asia/Kolkata"}
	scheduled := time.Date(2026, 8, 3, 21, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 4, 4, 0, 0, 0, time.UTC)

	next, ok, err := schedule.AdvancePast(scheduled, now)
	if err != nil || !ok {
		t.Fatalf("AdvancePast() = %v, %v, %v", next, ok, err)
	}
	want := time.Date(2026, 8, 4, 21, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}
