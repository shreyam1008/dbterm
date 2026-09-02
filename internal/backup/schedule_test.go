package backup

import (
	"encoding/json"
	"strings"
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

func TestScheduleNextDailyUsesMultipleWallClockTimes(t *testing.T) {
	schedule := Schedule{
		Kind:       ScheduleDaily,
		TimesOfDay: []string{"13:00", "01:00"},
		Timezone:   "Asia/Kolkata",
	}

	tests := []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		{
			name:  "later time on same local day",
			after: time.Date(2026, 8, 4, 4, 0, 0, 0, time.UTC), // 09:30 IST.
			want:  time.Date(2026, 8, 4, 7, 30, 0, 0, time.UTC),
		},
		{
			name:  "first time on next local day",
			after: time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC), // 13:30 IST.
			want:  time.Date(2026, 8, 4, 19, 30, 0, 0, time.UTC),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			next, ok, err := schedule.Next(test.after)
			if err != nil || !ok {
				t.Fatalf("Next() = %v, %v, %v", next, ok, err)
			}
			if !next.Equal(test.want) {
				t.Fatalf("next = %s, want %s", next, test.want)
			}
		})
	}
}

func TestScheduleWallClockTimesCanonicalizesDeduplicatesAndSorts(t *testing.T) {
	schedule := Schedule{
		Kind:       ScheduleDaily,
		TimeOfDay:  "not-used-when-plural-is-present",
		TimesOfDay: []string{"13:00", "1:00", "01:00", " 02:05 "},
	}
	times, err := schedule.WallClockTimes()
	if err != nil {
		t.Fatalf("WallClockTimes() error = %v", err)
	}
	want := []string{"01:00", "02:05", "13:00"}
	if len(times) != len(want) {
		t.Fatalf("times = %#v, want %#v", times, want)
	}
	for index := range want {
		if times[index] != want[index] {
			t.Fatalf("times = %#v, want %#v", times, want)
		}
	}
}

func TestSchedulePluralTimesAreAuthoritative(t *testing.T) {
	schedule := Schedule{
		Kind:       ScheduleDaily,
		TimeOfDay:  "03:00",
		TimesOfDay: []string{"13:00", "01:00"},
		Timezone:   "UTC",
	}
	after := time.Date(2026, 8, 4, 1, 30, 0, 0, time.UTC)

	next, ok, err := schedule.Next(after)
	if err != nil || !ok {
		t.Fatalf("Next() = %v, %v, %v", next, ok, err)
	}
	want := time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestScheduleValidatesEveryPluralWallClockTime(t *testing.T) {
	tests := []struct {
		name     string
		schedule Schedule
	}{
		{
			name:     "missing",
			schedule: Schedule{Kind: ScheduleDaily},
		},
		{
			name:     "blank plural entry",
			schedule: Schedule{Kind: ScheduleDaily, TimeOfDay: "02:00", TimesOfDay: []string{""}},
		},
		{
			name:     "one invalid among valid entries",
			schedule: Schedule{Kind: ScheduleDaily, TimesOfDay: []string{"01:00", "24:00", "13:00"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.schedule.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want invalid wall-clock time")
			}
		})
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

func TestScheduleNextWeeklyUsesMultipleWallClockTimes(t *testing.T) {
	schedule := Schedule{
		Kind:       ScheduleWeekly,
		TimesOfDay: []string{"17:00", "09:00"},
		Timezone:   "UTC",
		Weekdays:   []int{int(time.Monday), int(time.Friday)},
	}
	after := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC) // Friday.

	next, ok, err := schedule.Next(after)
	if err != nil || !ok {
		t.Fatalf("Next() = %v, %v, %v", next, ok, err)
	}
	want := time.Date(2026, 8, 7, 17, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestScheduleLegacyTimeOfDayJSONRemainsCompatible(t *testing.T) {
	var schedule Schedule
	if err := json.Unmarshal([]byte(`{"kind":"daily","time_of_day":"2:30","timezone":"UTC","run_missed_on_wake":true}`), &schedule); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	times, err := schedule.WallClockTimes()
	if err != nil {
		t.Fatalf("WallClockTimes() error = %v", err)
	}
	if len(times) != 1 || times[0] != "02:30" {
		t.Fatalf("times = %#v, want [02:30]", times)
	}
	next, ok, err := schedule.Next(time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC))
	if err != nil || !ok {
		t.Fatalf("Next() = %v, %v, %v", next, ok, err)
	}
	if want := time.Date(2026, 8, 4, 2, 30, 0, 0, time.UTC); !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestSchedulePluralTimesJSONUsesNewField(t *testing.T) {
	payload, err := json.Marshal(Schedule{Kind: ScheduleDaily, TimesOfDay: []string{"01:00", "13:00"}, Timezone: "UTC"})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	encoded := string(payload)
	if !strings.Contains(encoded, `"times_of_day":["01:00","13:00"]`) {
		t.Fatalf("JSON does not contain plural times: %s", encoded)
	}
	if strings.Contains(encoded, `"time_of_day"`) {
		t.Fatalf("empty legacy time should be omitted: %s", encoded)
	}
}

func TestScheduleNextSkipsNonexistentDSTWallClockTime(t *testing.T) {
	schedule := Schedule{Kind: ScheduleDaily, TimesOfDay: []string{"02:30"}, Timezone: "America/New_York"}
	// DST starts on 2026-03-08. 02:30 does not exist on that local day.
	after := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC) // 00:00 EST.

	next, ok, err := schedule.Next(after)
	if err != nil || !ok {
		t.Fatalf("Next() = %v, %v, %v", next, ok, err)
	}
	want := time.Date(2026, 3, 9, 6, 30, 0, 0, time.UTC) // 02:30 EDT next day.
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestScheduleNextUsesLaterValidTimeAfterDSTGap(t *testing.T) {
	schedule := Schedule{Kind: ScheduleDaily, TimesOfDay: []string{"02:30", "03:30"}, Timezone: "America/New_York"}
	after := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC) // 00:00 EST.

	next, ok, err := schedule.Next(after)
	if err != nil || !ok {
		t.Fatalf("Next() = %v, %v, %v", next, ok, err)
	}
	want := time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC) // 03:30 EDT.
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}

func TestScheduleNextRunsAmbiguousDSTWallClockOnce(t *testing.T) {
	schedule := Schedule{Kind: ScheduleDaily, TimesOfDay: []string{"01:30"}, Timezone: "America/New_York"}
	before := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC) // Before either 01:30 occurrence.

	first, ok, err := schedule.Next(before)
	if err != nil || !ok {
		t.Fatalf("first Next() = %v, %v, %v", first, ok, err)
	}
	second, ok, err := schedule.Next(first)
	if err != nil || !ok {
		t.Fatalf("second Next() = %v, %v, %v", second, ok, err)
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation() error = %v", err)
	}
	secondLocal := second.In(loc)
	if secondLocal.Year() != 2026 || secondLocal.Month() != time.November || secondLocal.Day() != 2 || secondLocal.Hour() != 1 || secondLocal.Minute() != 30 {
		t.Fatalf("second occurrence = %s; want next local day at 01:30", secondLocal)
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

func TestScheduleAdvancePastDailyUsesNextTimeOnSameDay(t *testing.T) {
	schedule := Schedule{Kind: ScheduleDaily, TimesOfDay: []string{"01:00", "13:00"}, Timezone: "Asia/Kolkata"}
	scheduled := time.Date(2026, 8, 3, 19, 30, 0, 0, time.UTC) // Aug 4, 01:00 IST.
	now := time.Date(2026, 8, 3, 22, 30, 0, 0, time.UTC)       // Aug 4, 04:00 IST.

	next, ok, err := schedule.AdvancePast(scheduled, now)
	if err != nil || !ok {
		t.Fatalf("AdvancePast() = %v, %v, %v", next, ok, err)
	}
	want := time.Date(2026, 8, 4, 7, 30, 0, 0, time.UTC) // Aug 4, 13:00 IST.
	if !next.Equal(want) {
		t.Fatalf("next = %s, want %s", next, want)
	}
}
