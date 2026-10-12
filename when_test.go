package main

import (
	"testing"
	"time"
)

func after(t *testing.T, text string) float64 {
	t.Helper()
	until, err := parseUntil(text, noon)
	if err != nil {
		t.Fatalf("%q: %v", text, err)
	}
	return until.Sub(noon).Seconds()
}

func TestParseUntilUnits(t *testing.T) {
	for text, want := range map[string]float64{
		"15m": 900, "1h": 3600, "1d": 86400, "1w": 604800, "2 hours": 7200, "1.5h": 5400,
	} {
		if got := after(t, text); got != want {
			t.Errorf("%q: %v, want %v", text, got, want)
		}
	}
}

func TestParseUntilCombinedAndBareMinutes(t *testing.T) {
	for _, text := range []string{"1h30m", "1h 30m", "90"} {
		if got := after(t, text); got != 5400 {
			t.Errorf("%q: %v", text, got)
		}
	}
}

func at(day, hour, minute int) time.Time {
	return time.Date(2026, 9, day, hour, minute, 0, 0, time.Local)
}

func TestParseUntilClockRollsToTomorrowWhenPassed(t *testing.T) {
	for text, want := range map[string]time.Time{
		"17:30": at(20, 17, 30), "9am": at(21, 9, 0), "9:15 pm": at(20, 21, 15), "12am": at(21, 0, 0),
		"tomorrow": at(21, 9, 0), "tomorrow 14:00": at(21, 14, 0),
	} {
		got, err := parseUntil(text, noon)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v, want %v", text, got, err, want)
		}
	}
}

func TestParseUntilRejectsGarbage(t *testing.T) {
	for _, text := range []string{"", "soon", "5x", "0m", "25:00", "13pm", "9999w", "h"} {
		if _, err := parseUntil(text, noon); err == nil {
			t.Errorf("%q: accepted", text)
		}
	}
}

// v0.2: "2h" across a DST change was 1h or 3h of real time.
func TestDurationsAreElapsedTimeAcrossDaylightSaving(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Skip("no tz database:", err)
	}
	for _, start := range []time.Time{
		time.Date(2026, 3, 29, 1, 30, 0, 0, madrid), time.Date(2026, 10, 25, 1, 30, 0, 0, madrid),
	} {
		until, _ := parseUntil("2h", start)
		if d := until.Sub(start); d != 2*time.Hour {
			t.Errorf("%v: %v", start, d)
		}
	}
	// a clock time stays a clock time
	if until, _ := parseUntil("9am", time.Date(2026, 3, 29, 1, 30, 0, 0, madrid)); until.Hour() != 9 {
		t.Errorf("9am: %v", until)
	}
}

func TestBadged(t *testing.T) {
	// an emoji badge covers one space in herdr's sidebar, so it gets two
	for _, c := range [][3]string{{"💤", "1", "💤  1"}, {"z", "1", "z 1"}, {"", "1", "1"}, {"zz💤", "17:30", "zz💤  17:30"}} {
		if got := badged(c[0], c[1]); got != c[2] {
			t.Errorf("badged(%q, %q) = %q", c[0], c[1], got)
		}
	}
}

func TestDescribe(t *testing.T) {
	for spec, want := range map[string]string{
		"15m": "15 minutes", "1h": "1 hour", "1h30m": "1 hour 30 minutes", "1w": "1 week", "17:30": "until 17:30",
	} {
		if got := describe(spec); got != want {
			t.Errorf("describe(%q) = %q", spec, got)
		}
	}
}

func TestFormatUntil(t *testing.T) {
	for _, c := range []struct {
		until time.Time
		want  string
	}{
		{at(20, 17, 5), "17:05"}, {at(22, 9, 0), "Tue 09:00"},
		{time.Date(2026, 10, 4, 9, 0, 0, 0, time.Local), "4 Oct 09:00"},
	} {
		if got := formatUntil(c.until, noon); got != c.want {
			t.Errorf("%v: %q", c.until, got)
		}
	}
}

func TestFormatLeft(t *testing.T) {
	for ms, want := range map[int64]string{
		45 * 1000: "45s", 12 * 60 * 1000: "12m", 2*hourMS + 14*60*1000: "2h 14m", 76 * hourMS: "3d 4h", -5: "0s",
		// rounds up: a snooze just set reads "3m", not "2m"
		3*60*1000 - 800: "3m", 60*60*1000 - 1: "1h", 24*hourMS - 1: "1d", 1: "1s",
	} {
		if got := formatLeft(ms); got != want {
			t.Errorf("formatLeft(%d) = %q, want %q", ms, got, want)
		}
	}
}
