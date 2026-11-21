package triggers

import (
	"math/bits"
	"strings"
	"time"
)

const cronHorizonYears = 5

type cronSchedule struct {
	seconds uint64
	minutes uint64
	hours   uint64
	doms    uint64
	months  uint64
	dows    uint64

	domRestricted bool
	dowRestricted bool
}

// parseCron accepts the classic 5-field form (minute hour dom month dow) and
// croniter's 6-field form, where the trailing field is seconds. Every Python
// caller (framework/tools/queen_lifecycle_tools.py, framework/host/triggers.py)
// builds croniter with its default second_at_beginning=False, so seconds are
// always the last field; the leading-second layout (second_at_beginning=True)
// is deliberately not accepted here because no upstream caller produces it.
//
// Unsupported croniter extensions (names, macros, L/W/#, ?, wrap-around
// ranges and the 7-field year form) are rejected rather than guessed at, so
// the parser stays a strict subset of what Python accepts.
func parseCron(expr string) (*cronSchedule, bool) {
	fields := strings.Fields(expr)
	if len(fields) != 5 && len(fields) != 6 {
		return nil, false
	}

	// A 5-field expression fires at second 0 of every matching minute, which
	// is exactly what croniter does when no seconds field is supplied.
	seconds := uint64(1)
	if len(fields) == 6 {
		bits, ok := parseCronField(fields[5], 0, 59, 59, false)
		if !ok {
			return nil, false
		}
		seconds = bits
	}

	minutes, ok := parseCronField(fields[0], 0, 59, 59, false)
	if !ok {
		return nil, false
	}
	hours, ok := parseCronField(fields[1], 0, 23, 23, false)
	if !ok {
		return nil, false
	}
	doms, ok := parseCronField(fields[2], 1, 31, 31, false)
	if !ok {
		return nil, false
	}
	months, ok := parseCronField(fields[3], 1, 12, 12, false)
	if !ok {
		return nil, false
	}
	// croniter's day-of-week range is 0-6; the classic 5-field form accepts 7
	// as an alias for 0, but the 6-field form does not (value_alias is
	// disabled for DOW whenever a seconds field is present) and rejects it.
	dows, ok := parseCronField(fields[4], 0, 7, 6, len(fields) == 5)
	if !ok {
		return nil, false
	}

	// croniter aliases day-of-week 7 to Sunday (0) and treats 0-7 as the full
	// week, so normalise before the wildcard/full-range checks below.
	if dows&(1<<7) != 0 {
		if len(fields) == 6 {
			return nil, false
		}
		dows |= 1 << 0
	}
	dows &= ^(uint64(1) << 7)

	// croniter's day_or rule (Vixie cron): date and weekday are ORed only when
	// neither expanded field collapsed to "*". A field collapses when it is a
	// bare "*" or when it covers the whole domain and the other day field
	// contains a "*". Mirroring that exactly matters for expressions such as
	// "0 0 */2 * 1": croniter sees */2 as a restricted day-of-month list and
	// ORs it with Monday, while the old syntactic-star check wrongly ANDed.
	domFull := bits.OnesCount64(doms) == 31
	dowFull := bits.OnesCount64(dows) == 7
	domStar := containsBareStar(fields[2]) || (domFull && strings.Contains(fields[4], "*"))
	dowStar := containsBareStar(fields[4]) || (dowFull && strings.Contains(fields[2], "*"))

	return &cronSchedule{
		seconds:       seconds,
		minutes:       minutes,
		hours:         hours,
		doms:          doms,
		months:        months,
		dows:          dows,
		domRestricted: !domStar,
		dowRestricted: !dowStar,
	}, true
}

func containsBareStar(field string) bool {
	for _, part := range strings.Split(field, ",") {
		if part == "*" {
			return true
		}
	}
	return false
}

// parseCronNumber accepts only unsigned decimal digits, matching croniter's
// only_int_re. strconv.Atoi would also accept "+1", which croniter rejects.
func parseCronNumber(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
		if n > 1<<30 {
			return 0, false
		}
	}
	return n, true
}

func parseCronField(field string, min, max, normMax int, allowSundayAlias bool) (uint64, bool) {
	if field == "" {
		return 0, false
	}
	dowField := max == 7
	var set uint64
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return 0, false
		}
		base := part
		step := 1
		hasStep := false
		if i := strings.Index(part, "/"); i >= 0 {
			hasStep = true
			base = part[:i]
			n, ok := parseCronNumber(part[i+1:])
			if !ok || n <= 0 {
				return 0, false
			}
			step = n
		}

		var lo, hi int
		switch {
		case base == "*":
			lo, hi = min, normMax
		case strings.Contains(base, "-"):
			bounds := strings.SplitN(base, "-", 2)
			loN, ok1 := parseCronNumber(bounds[0])
			hiN, ok2 := parseCronNumber(bounds[1])
			if !ok1 || !ok2 {
				return 0, false
			}
			if loN < min || hiN > max {
				return 0, false
			}
			if dowField && !allowSundayAlias && (loN == 7 || hiN == 7) {
				return 0, false
			}
			if loN > hiN {
				// croniter supports wrapping ranges (e.g. 5-1); this parser is
				// deliberately stricter, so keep rejecting them.
				return 0, false
			}
			if loN == hiN {
				// croniter quirk: a range whose bounds are equal means the
				// whole field cycle, not a single value (e.g. "Sun-Sun").
				lo, hi = min, normMax
			} else {
				lo, hi = loN, hiN
			}
		default:
			n, ok := parseCronNumber(base)
			if !ok || n < min || n > max {
				return 0, false
			}
			if dowField && !allowSundayAlias && n == 7 {
				return 0, false
			}
			lo, hi = n, n
			if hasStep {
				// croniter rewrites "a/n" to "a-RANGES_MAX/n" even when n == 1.
				// Reaching the field maximum makes the bounds equal, which
				// croniter expands as the whole cycle instead of one value
				// (e.g. "59/5" minutes means 0,5,...,55; DOW's max is 6).
				if n >= normMax {
					lo, hi = min, normMax
				} else {
					hi = normMax
				}
			}
		}

		for v := lo; v <= hi; v += step {
			set |= uint64(1) << uint(v)
		}
	}
	return set, true
}

func IsValidCron(expr string) bool {
	_, ok := parseCron(expr)
	return ok
}

// NextCronMatch returns the first schedule match strictly after `after`,
// mirroring croniter.get_next: the anchor is floored to whole seconds and the
// result may land on any second of a matching minute when the expression
// carries a seconds field.
func NextCronMatch(expr string, after time.Time) (time.Time, bool) {
	sched, ok := parseCron(expr)
	if !ok {
		return time.Time{}, false
	}
	return sched.next(after)
}

func (s *cronSchedule) next(after time.Time) (time.Time, bool) {
	candidate := after.Truncate(time.Second).Add(time.Second)
	horizon := after.AddDate(cronHorizonYears, 0, 0)

	minute := candidate.Truncate(time.Minute)
	firstSecond := candidate.Second()
	for !minute.After(horizon) {
		if s.matchesMinute(minute) {
			for sec := firstSecond; sec < 60; sec++ {
				if s.seconds&(uint64(1)<<uint(sec)) == 0 {
					continue
				}
				match := minute.Add(time.Duration(sec) * time.Second)
				if match.After(horizon) {
					return time.Time{}, false
				}
				return match, true
			}
		}
		minute = minute.Add(time.Minute)
		firstSecond = 0
	}
	return time.Time{}, false
}

// matches reports whether t is an exact schedule tick (seconds included).
func (s *cronSchedule) matches(t time.Time) bool {
	if s.seconds&(uint64(1)<<uint(t.Second())) == 0 {
		return false
	}
	return s.matchesMinute(t)
}

func (s *cronSchedule) matchesMinute(t time.Time) bool {
	if s.minutes&(uint64(1)<<uint(t.Minute())) == 0 {
		return false
	}
	if s.hours&(uint64(1)<<uint(t.Hour())) == 0 {
		return false
	}
	if s.months&(uint64(1)<<uint(int(t.Month()))) == 0 {
		return false
	}

	domMatch := s.doms&(uint64(1)<<uint(t.Day())) != 0
	dowMatch := s.dows&(uint64(1)<<uint(int(t.Weekday()))) != 0
	if s.domRestricted && s.dowRestricted {
		return domMatch || dowMatch
	}
	return domMatch && dowMatch
}
