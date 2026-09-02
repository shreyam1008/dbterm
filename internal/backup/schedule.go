package backup

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type ScheduleKind string

const (
	ScheduleManual   ScheduleKind = "manual"
	ScheduleInterval ScheduleKind = "interval"
	ScheduleDaily    ScheduleKind = "daily"
	ScheduleWeekly   ScheduleKind = "weekly"
)

type Schedule struct {
	Kind            ScheduleKind `json:"kind"`
	EveryMinutes    int          `json:"every_minutes,omitempty"`
	TimeOfDay       string       `json:"time_of_day,omitempty"`
	TimesOfDay      []string     `json:"times_of_day,omitempty"`
	Weekdays        []int        `json:"weekdays,omitempty"`
	Timezone        string       `json:"timezone,omitempty"`
	RunMissedOnWake bool         `json:"run_missed_on_wake"`
}

func (s Schedule) Validate() error {
	switch s.Kind {
	case "", ScheduleManual:
		return nil
	case ScheduleInterval:
		if s.EveryMinutes < 5 || s.EveryMinutes > 365*24*60 {
			return fmt.Errorf("interval must be between 5 minutes and 365 days")
		}
	case ScheduleDaily:
		if _, err := s.wallClockTimes(); err != nil {
			return err
		}
	case ScheduleWeekly:
		if _, err := s.wallClockTimes(); err != nil {
			return err
		}
		if len(s.Weekdays) == 0 {
			return fmt.Errorf("weekly schedule requires at least one weekday")
		}
		for _, day := range s.Weekdays {
			if day < int(time.Sunday) || day > int(time.Saturday) {
				return fmt.Errorf("weekday must be between 0 (Sunday) and 6 (Saturday)")
			}
		}
	default:
		return fmt.Errorf("unsupported schedule %q", s.Kind)
	}
	_, err := s.location()
	return err
}

func (s Schedule) Next(after time.Time) (time.Time, bool, error) {
	if err := s.Validate(); err != nil {
		return time.Time{}, false, err
	}
	if s.Kind == "" || s.Kind == ScheduleManual {
		return time.Time{}, false, nil
	}
	if after.IsZero() {
		after = time.Now()
	}
	if s.Kind == ScheduleInterval {
		return after.Add(time.Duration(s.EveryMinutes) * time.Minute).UTC(), true, nil
	}

	loc, _ := s.location()
	localAfter := after.In(loc)
	times, _ := s.wallClockTimes()
	allowed := func(day time.Weekday) bool {
		if s.Kind == ScheduleDaily {
			return true
		}
		for _, candidate := range s.Weekdays {
			if candidate == int(day) {
				return true
			}
		}
		return false
	}

	for offset := 0; offset <= 8; offset++ {
		date := localAfter.AddDate(0, 0, offset)
		if !allowed(date.Weekday()) {
			continue
		}
		for _, clock := range times {
			hour, minute, _ := parseClock(clock)
			candidate := time.Date(date.Year(), date.Month(), date.Day(), hour, minute, 0, 0, loc)
			// time.Date normalizes a nonexistent local time during a DST gap.
			// Skipping that occurrence is safer than silently moving a backup to a
			// different wall-clock time. Ambiguous fall-back times execute once.
			candidateLocal := candidate.In(loc)
			if candidateLocal.Year() != date.Year() || candidateLocal.Month() != date.Month() ||
				candidateLocal.Day() != date.Day() || candidateLocal.Hour() != hour || candidateLocal.Minute() != minute {
				continue
			}
			if candidate.After(localAfter) {
				return candidate.UTC(), true, nil
			}
		}
	}
	return time.Time{}, false, fmt.Errorf("could not calculate the next scheduled run")
}

// WallClockTimes returns the effective daily/weekly wall-clock times in
// canonical HH:MM order. The plural field is authoritative when present;
// TimeOfDay remains a backward-compatible fallback for existing catalogs.
func (s Schedule) WallClockTimes() ([]string, error) {
	return s.wallClockTimes()
}

func (s Schedule) wallClockTimes() ([]string, error) {
	values := s.TimesOfDay
	field := "times_of_day"
	if len(values) == 0 {
		values = []string{s.TimeOfDay}
		field = "time_of_day"
	}

	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		hour, minute, err := parseClock(value)
		if err != nil {
			return nil, fmt.Errorf("%s value %q: %w", field, value, err)
		}
		unique[fmt.Sprintf("%02d:%02d", hour, minute)] = struct{}{}
	}
	if len(unique) == 0 {
		return nil, fmt.Errorf("schedule requires at least one wall-clock time")
	}

	times := make([]string, 0, len(unique))
	for value := range unique {
		times = append(times, value)
	}
	sort.Strings(times)
	return times, nil
}

// AdvancePast returns the first scheduled occurrence strictly after now while
// preserving the original cadence for interval schedules. It is used when a
// sleeping agent intentionally skips an overdue run.
func (s Schedule) AdvancePast(scheduled, now time.Time) (time.Time, bool, error) {
	if err := s.Validate(); err != nil {
		return time.Time{}, false, err
	}
	if s.Kind == "" || s.Kind == ScheduleManual {
		return time.Time{}, false, nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	if scheduled.IsZero() {
		return s.Next(now)
	}
	if scheduled.After(now) {
		return scheduled.UTC(), true, nil
	}

	if s.Kind == ScheduleInterval {
		interval := time.Duration(s.EveryMinutes) * time.Minute
		steps := now.Sub(scheduled)/interval + 1
		return scheduled.Add(steps * interval).UTC(), true, nil
	}

	// Daily and weekly schedules are anchored to wall-clock time, so asking for
	// the next occurrence after now preserves their timezone and DST behavior.
	return s.Next(now)
}

func (s Schedule) location() (*time.Location, error) {
	name := strings.TrimSpace(s.Timezone)
	if name == "" || strings.EqualFold(name, "local") {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown timezone %q: %w", name, err)
	}
	return loc, nil
}

func parseClock(value string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("time must use 24-hour HH:MM format")
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("hour must be between 00 and 23")
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("minute must be between 00 and 59")
	}
	return hour, minute, nil
}
