// Package cron is the scheduled-job store, schedule parser, scheduler and the cronjob tool. Schedules are
// durations, "every" phrases, 5-field cron expressions and ISO one-shots (the Python forms); cron expressions are
// evaluated on the wall clock of the configured time zone, so a 09:00 job stays at 09:00 across DST changes.
package cron

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Schedule kinds.
const (
	KindOnce     = "once"
	KindInterval = "interval"
	KindCron     = "cron"
)

// Schedule is a parsed schedule. The JSON shape is Python's.
type Schedule struct {
	Kind    string `json:"kind"`
	Expr    string `json:"expr,omitempty"`
	Minutes *int   `json:"minutes,omitempty"`
	RunAt   string `json:"run_at,omitempty"`
	Display string `json:"display"`
}

var durationRe = regexp.MustCompile(`^(\d*)\s*(m|min|mins|minute|minutes|h|hr|hrs|hour|hours|d|day|days)$`)

// ParseDuration reads a duration in minutes: "30m", "2h", "1d", or a bare unit ("hour" is one hour).
func ParseDuration(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	m := durationRe.FindStringSubmatch(s)
	if m == nil {
		return 0, msgErr("Invalid duration: '%s'. Use format like '30m', '2h', '1d', or a bare unit like 'hour' (defaults to 1).", s)
	}
	n := 1
	if m[1] != "" {
		n, _ = strconv.Atoi(m[1])
	}
	return n * map[byte]int{'m': 1, 'h': 60, 'd': 1440}[m[2][0]], nil
}

var weekdayToDow = map[string]string{
	"sunday": "0", "sun": "0", "monday": "1", "mon": "1", "tuesday": "2", "tue": "2", "tues": "2",
	"wednesday": "3", "wed": "3", "weds": "3", "thursday": "4", "thu": "4", "thur": "4", "thurs": "4",
	"friday": "5", "fri": "5", "saturday": "6", "sat": "6",
}

var dayspecToDow = map[string]string{"day": "*", "daily": "*", "everyday": "*", "weekday": "1-5", "weekdays": "1-5", "weekend": "0,6", "weekends": "0,6"}

var clockRe = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?(am|pm)?$`)

// parseClock reads 9am, 9:30am, 14:00, 7 (a bare 24-hour hour), noon, midnight.
func parseClock(text string) (h, m int, ok bool) {
	t := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(text)), " ", "")
	switch t {
	case "":
		return 0, 0, false
	case "noon", "midday":
		return 12, 0, true
	case "midnight":
		return 0, 0, true
	}
	g := clockRe.FindStringSubmatch(t)
	if g == nil {
		return 0, 0, false
	}
	h, _ = strconv.Atoi(g[1])
	if g[2] != "" {
		m, _ = strconv.Atoi(g[2])
	}
	if g[3] != "" {
		if h < 1 || h > 12 {
			return 0, 0, false
		}
		h = h % 12
		if g[3] == "pm" {
			h += 12
		}
	}
	if h > 23 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// naturalEveryToCron converts "<when> [at] <time>" ("monday 9am", "weekday at 9am", "mon, wed and fri at 6pm") to a
// cron expression, or "" when it is not one (the caller then tries an interval).
func naturalEveryToCron(rest string) string {
	tokens := strings.Fields(strings.ReplaceAll(strings.ToLower(rest), ",", " "))
	if len(tokens) == 0 {
		return ""
	}
	dow, idx := dayspecToDow[tokens[0]], 1
	if dow == "" {
		var days []string
		idx = len(tokens)
		for i, tok := range tokens {
			if tok == "and" {
				continue
			}
			mapped, ok := weekdayToDow[tok]
			if !ok {
				idx = i
				break
			}
			dup := false
			for _, d := range days {
				dup = dup || d == mapped
			}
			if !dup {
				days = append(days, mapped)
			}
		}
		if len(days) == 0 {
			return ""
		}
		dow = strings.Join(days, ",")
	}
	timeTokens := tokens[idx:]
	if len(timeTokens) > 0 && timeTokens[0] == "at" {
		timeTokens = timeTokens[1:]
	}
	if len(timeTokens) == 0 {
		return ""
	}
	h, m, ok := parseClock(strings.Join(timeTokens, " "))
	if !ok {
		return ""
	}
	return fmt.Sprintf("%d %d * * %s", m, h, dow)
}

var cronFieldRe = regexp.MustCompile(`^[A-Za-z\d*\-,/]+$`)

// Iso formats like Python's datetime.isoformat(): microseconds only when non-zero, offset as +HH:MM.
func Iso(t time.Time) string {
	s := t.Format("2006-01-02T15:04:05")
	if us := t.Nanosecond() / 1000; us != 0 {
		s += fmt.Sprintf(".%06d", us)
	}
	return s + t.Format("-07:00")
}

func isoLayouts() []string {
	return []string{"2006-01-02T15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04Z07:00", "2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02"}
}

// parseISO reads an ISO timestamp; one without an offset is read in loc (the configured zone), not server-local.
func parseISO(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, l := range isoLayouts() {
		if t, err := time.ParseInLocation(l, s, loc); err == nil {
			return t, nil
		}
	}
	// Python reports the first complaint; month and day ranges are the common ones.
	if m := regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})`).FindStringSubmatch(s); m != nil {
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		switch {
		case mo < 1 || mo > 12:
			return time.Time{}, fmt.Errorf("month must be in 1..12, not %d", mo)
		case d < 1 || d > 31:
			return time.Time{}, fmt.Errorf("day is out of range for month")
		}
	}
	return time.Time{}, msgErr("Invalid isoformat string: '%s'", s)
}

const scheduleHelp = "Use:\n" +
	"  - Interval: '30m', 'every 30m', 'every 2h' (recurring)\n" +
	"  - One-shot delay: 'in 30m', 'in 2h' (fires once)\n" +
	"  - Weekly/daily: 'every monday 9am', 'weekdays at 9am' (recurring)\n" +
	"  - Cron: '0 9 * * *' (cron expression)\n" +
	"  - Timestamp: '2026-02-03T14:00:00' (one-shot at time)"

func interval(minutes int) Schedule {
	return Schedule{Kind: KindInterval, Minutes: &minutes, Display: fmt.Sprintf("every %dm", minutes)}
}

func cronSchedule(expr, display, label string) (Schedule, error) {
	if _, err := ParseCron(expr); err != nil {
		return Schedule{}, msgErr("Invalid %s '%s': %v", label, display, err)
	}
	return Schedule{Kind: KindCron, Expr: expr, Display: display}, nil
}

// ParseSchedule parses a schedule string. "30m" and "every 30m" are recurring intervals; "every monday 9am" and
// "0 9 * * *" are cron; an ISO timestamp or "in 30m" is a one-shot. now supplies the zone and the reference instant.
func ParseSchedule(schedule string, now time.Time) (Schedule, error) {
	schedule = strings.TrimSpace(schedule)
	original, lower := schedule, strings.ToLower(schedule)
	isEvery := strings.HasPrefix(lower, "every ")
	rest := lower
	if isEvery {
		rest = strings.TrimSpace(schedule[6:])
	}
	if expr := naturalEveryToCron(rest); expr != "" {
		return cronSchedule(expr, original, "schedule")
	}
	if isEvery {
		m, err := ParseDuration(rest)
		if err != nil {
			return Schedule{}, err
		}
		return interval(m), nil
	}
	parts := strings.Fields(schedule)
	if len(parts) >= 5 {
		all := true
		for _, p := range parts[:5] {
			all = all && cronFieldRe.MatchString(p)
		}
		if all {
			return cronSchedule(schedule, schedule, "cron expression")
		}
	}
	if strings.Contains(schedule, "T") || regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`).MatchString(schedule) {
		t, err := parseISO(schedule, now.Location())
		if err != nil {
			return Schedule{}, msgErr("Invalid timestamp '%s': %v", schedule, err)
		}
		return Schedule{Kind: KindOnce, RunAt: Iso(t), Display: "once at " + t.Format("2006-01-02 15:04")}, nil
	}
	if strings.HasPrefix(lower, "in ") {
		d := strings.TrimSpace(schedule[3:])
		m, err := ParseDuration(d)
		if err != nil {
			return Schedule{}, msgErr("Invalid duration '%s' after 'in '. Use e.g. 'in 30m', 'in 2h'.", d)
		}
		return Schedule{Kind: KindOnce, RunAt: Iso(now.Add(time.Duration(m) * time.Minute)), Display: "once in " + d}, nil
	}
	if m, err := ParseDuration(schedule); err == nil {
		return interval(m), nil
	}
	return Schedule{}, msgErr("Invalid schedule '%s'. %s", original, scheduleHelp)
}

// msgErr builds an error whose text is shown to the model verbatim: Python's wording, capital letters and all, so it
// is not a Go-style error string.
func msgErr(format string, a ...any) error { return textError(fmt.Sprintf(format, a...)) }

type textError string

func (e textError) Error() string { return string(e) }
