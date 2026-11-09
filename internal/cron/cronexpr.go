package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Expr is a parsed 5-field cron expression (minute hour day-of-month month day-of-week).
type Expr struct {
	minute, hour, dom, month, dow []bool // dow has 7 slots: 0 is Sunday (7 folds into it)
	domStar, dowStar              bool
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
var dowNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

func parseValue(tok string, names map[string]int, lo, hi int) (int, error) {
	if n, ok := names[strings.ToLower(tok)]; ok {
		return n, nil
	}
	n, err := strconv.Atoi(tok)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("out of range")
	}
	return n, nil
}

// parseField fills set (size hi+1) for one field. hi is inclusive.
func parseField(field string, names map[string]int, lo, hi int) ([]bool, error) {
	set := make([]bool, hi+1)
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return nil, fmt.Errorf("empty list item")
		}
		step := 1
		if base, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("bad step")
			}
			step, part = n, base
		}
		from, to := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			var err error
			if from, err = parseValue(a, names, lo, hi); err != nil {
				return nil, err
			}
			if to, err = parseValue(b, names, lo, hi); err != nil {
				return nil, err
			}
			if from > to {
				return nil, fmt.Errorf("range start is after its end")
			}
		default:
			v, err := parseValue(part, names, lo, hi)
			if err != nil {
				return nil, err
			}
			from, to = v, v
			if step != 1 || strings.Contains(field, "/") {
				to = hi // "5/15" runs from 5 to the end of the range
			}
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	return set, nil
}

// ParseCron validates and parses a 5-field cron expression. Names (JAN-DEC, SUN-SAT) are accepted, and 7 means
// Sunday. Day-of-month and day-of-week combine with OR when both are restricted.
func ParseCron(expr string) (*Expr, error) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("[%s] is not acceptable: exactly 5 fields are supported", expr)
	}
	bad := func(err error) error { return fmt.Errorf("[%s] is not acceptable, %v", expr, err) }
	e := &Expr{}
	var err error
	if e.minute, err = parseField(f[0], nil, 0, 59); err != nil {
		return nil, bad(err)
	}
	if e.hour, err = parseField(f[1], nil, 0, 23); err != nil {
		return nil, bad(err)
	}
	if e.dom, err = parseField(f[2], nil, 1, 31); err != nil {
		return nil, bad(err)
	}
	if e.month, err = parseField(f[3], monthNames, 1, 12); err != nil {
		return nil, bad(err)
	}
	dow, err := parseField(f[4], dowNames, 0, 7)
	if err != nil {
		return nil, bad(err)
	}
	e.dow = make([]bool, 7)
	for i, on := range dow {
		if on {
			e.dow[i%7] = true
		}
	}
	e.domStar, e.dowStar = strings.HasPrefix(f[2], "*"), strings.HasPrefix(f[4], "*")
	return e, nil
}

func (e *Expr) dayMatches(t time.Time) bool {
	dom, dow := e.dom[t.Day()], e.dow[int(t.Weekday())]
	switch {
	case e.domStar && e.dowStar:
		return true
	case e.domStar:
		return dow
	case e.dowStar:
		return dom
	}
	return dom || dow
}

// Next returns the first instant strictly after base whose wall-clock time in loc matches. The match is made on
// the wall clock, so "0 9 * * *" is 09:00 local every day, DST or not. A wall time inside a spring-forward gap is
// skipped to the instant the clock jumps to; a repeated autumn hour fires once per matching wall time after base's
// wall clock (never twice for the same wall time). ok is false when nothing matches within eight years.
func (e *Expr) Next(base time.Time, loc *time.Location) (time.Time, bool) {
	wall := base.In(loc)
	// Work on the wall clock as a UTC-labelled value: no zone arithmetic can disturb the field walk.
	cur := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, time.UTC).Add(time.Minute)
	end := cur.AddDate(8, 0, 0)
	for day := time.Date(cur.Year(), cur.Month(), cur.Day(), 0, 0, 0, 0, time.UTC); day.Before(end); day = day.AddDate(0, 0, 1) {
		if !e.month[int(day.Month())] || !e.dayMatches(day) {
			continue
		}
		for h := 0; h < 24; h++ {
			if !e.hour[h] {
				continue
			}
			for m := 0; m < 60; m++ {
				if !e.minute[m] {
					continue
				}
				w := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, time.UTC)
				if w.Before(cur) {
					continue
				}
				if t, ok := resolve(w, loc, base); ok {
					return t, true
				}
			}
		}
	}
	return time.Time{}, false
}

// resolve turns a wall time into an instant in loc strictly after base. A repeated hour has two instants; the
// earlier is tried first, then the later.
func resolve(w time.Time, loc *time.Location, base time.Time) (time.Time, bool) {
	t := time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), 0, 0, loc)
	if g := t.In(loc); g.Hour() != w.Hour() || g.Minute() != w.Minute() {
		// The wall time falls in a spring-forward gap and Go moved it back. Python applies the offset in force
		// before the jump, which lands one shift later: that is when the clock reads past the gap.
		t = t.Add(time.Hour)
	}
	if t.After(base) {
		return t, true
	}
	// The later occurrence of a repeated wall time: one DST shift after the first.
	for _, shift := range []time.Duration{time.Hour, 30 * time.Minute} {
		alt := t.Add(shift)
		if alt.In(loc).Hour() == w.Hour() && alt.In(loc).Minute() == w.Minute() && alt.After(base) {
			return alt, true
		}
	}
	return time.Time{}, false
}
