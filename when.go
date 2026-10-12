package main

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

const maxSnooze = 366 * 24 * time.Hour

var units = map[string]float64{
	"s": 1, "sec": 1, "secs": 1, "second": 1, "seconds": 1,
	"m": 60, "min": 60, "mins": 60, "minute": 60, "minutes": 60,
	"h": 3600, "hr": 3600, "hrs": 3600, "hour": 3600, "hours": 3600,
	"d": 86400, "day": 86400, "days": 86400,
	"w": 604800, "wk": 604800, "wks": 604800, "week": 604800, "weeks": 604800,
}

var unitNames = []struct {
	size int64
	name string
}{{604800, "week"}, {86400, "day"}, {3600, "hour"}, {60, "minute"}, {1, "second"}}

var (
	partRE  = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)\s*([a-z]*)`)
	clockRE = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)
)

// parseClock reads '17:30', '9am', '9:15 pm' as an hour and a minute. A bare
// number is not a clock.
func parseClock(text string) (hour, minute int, ok bool) {
	m := clockRE.FindStringSubmatch(text)
	if m == nil || (m[2] == "" && m[3] == "") {
		return 0, 0, false
	}
	hour, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	if m[3] != "" {
		if hour < 1 || hour > 12 {
			return 0, 0, false
		}
		hour %= 12
		if m[3] == "pm" {
			hour += 12
		}
	}
	if hour > 23 || minute > 59 {
		return 0, 0, false
	}
	return hour, minute, true
}

// parseSeconds reads a duration: '15m', '1h30m', '2 days', '90' (minutes).
func parseSeconds(text string) (float64, error) {
	seconds, pos := 0.0, 0
	for pos < len(text) {
		m := partRE.FindStringSubmatchIndex(text[pos:])
		if m == nil || m[1] == 0 {
			return 0, fmt.Errorf("can't read '%s' — try 45m, 2h, 3d, 1w or 17:30", text)
		}
		num, _ := strconv.ParseFloat(text[pos+m[2]:pos+m[3]], 64)
		unit := text[pos+m[4] : pos+m[5]]
		if unit == "" {
			unit = "m"
		}
		size, ok := units[unit]
		if !ok {
			return 0, fmt.Errorf("unknown unit '%s' — use s, m, h, d or w", unit)
		}
		seconds += num * size
		pos += m[1]
	}
	if seconds <= 0 {
		return 0, errors.New("duration must be more than zero")
	}
	if seconds > maxSnooze.Seconds() {
		return 0, errors.New("that's more than a year — close the pane instead?")
	}
	return seconds, nil
}

// parseUntil turns '15m', '1h30m', '2 days', '90' (minutes), '17:30', '9am'
// or 'tomorrow [9am]' into a deadline. Its errors are for people to read.
func parseUntil(text string, now time.Time) (time.Time, error) {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return time.Time{}, errors.New("enter a duration like 45m, 2h, 3d or a time like 17:30")
	}
	if rest, ok := strings.CutPrefix(text, "tomorrow"); ok {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			rest = "9:00"
		}
		h, m, ok := parseClock(rest)
		if !ok {
			return time.Time{}, errors.New("try 'tomorrow' or 'tomorrow 14:00'")
		}
		return time.Date(now.Year(), now.Month(), now.Day()+1, h, m, 0, 0, now.Location()), nil
	}
	if h, m, ok := parseClock(text); ok {
		until := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
		if !until.After(now) {
			until = time.Date(now.Year(), now.Month(), now.Day()+1, h, m, 0, 0, now.Location())
		}
		return until, nil
	}
	seconds, err := parseSeconds(text)
	if err != nil {
		return time.Time{}, err
	}
	// Elapsed time, not wall-clock arithmetic: across a daylight-saving
	// change "2h" must still mean two hours from now (a clock time like 9am
	// above stays a clock time).
	return now.Add(time.Duration(seconds * float64(time.Second))), nil
}

// describe is a configured duration as the menu says it: '15m' is
// '15 minutes'.
func describe(spec string) string {
	text := strings.ToLower(strings.TrimSpace(spec))
	if strings.HasPrefix(text, "tomorrow") {
		return text
	}
	if _, _, ok := parseClock(text); ok {
		return "until " + text
	}
	seconds, err := parseSeconds(text)
	if err != nil {
		return text
	}
	total := int64(seconds)
	var parts []string
	for _, u := range unitNames {
		if n := total / u.size; n > 0 {
			total -= n * u.size
			s := "s"
			if n == 1 {
				s = ""
			}
			parts = append(parts, fmt.Sprintf("%d %s%s", n, u.name, s))
		}
	}
	return strings.Join(parts, " ")
}

// formatUntil is a deadline as short as it can be said: the time today, the
// weekday this week, the date beyond.
func formatUntil(until, now time.Time) string {
	y1, m1, d1 := until.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return until.Format("15:04")
	case until.Sub(now) < 6*24*time.Hour:
		return until.Format("Mon 15:04")
	}
	return until.Format("2 Jan 15:04")
}

// formatLeft is time left, rounded up: a 3-minute snooze reads "3m" the
// moment it is set, not "2m".
func formatLeft(ms int64) string {
	seconds := max(0, int64(math.Ceil(float64(ms)/1000)))
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := (seconds + 59) / 60
	if minutes < 60 {
		return fmt.Sprintf("%dm", minutes)
	}
	hours, minutes := minutes/60, minutes%60
	if hours < 24 {
		if minutes > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	}
	days, hours := hours/24, hours%24
	if hours > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	return fmt.Sprintf("%dd", days)
}

// badged is the badge, a gap, then text. A wide badge (an emoji such as 💤)
// gets two spaces: herdr's sidebar lets it cover the single space after it,
// so "💤 1" shows as "💤1".
func badged(badge, text string) string {
	if badge == "" {
		return text
	}
	rs := []rune(badge)
	if runewidth.RuneWidth(rs[len(rs)-1]) == 2 {
		return badge + "  " + text
	}
	return badge + " " + text
}

func nowMS() int64 { return time.Now().UnixMilli() }

func fromMS(ms int64) time.Time { return time.UnixMilli(ms) }
