package triggers

import (
	"strconv"
	"strings"
	"time"
)

type cronSchedule struct {
	minutes uint64
	hours   uint64
	doms    uint64
	months  uint64
	dows    uint64

	domRestricted bool
	dowRestricted bool
}

func parseCron(expr string) (*cronSchedule, bool) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return nil, false
	}

	minutes, _, ok := parseCronField(fields[0], 0, 59)
	if !ok {
		return nil, false
	}
	hours, _, ok := parseCronField(fields[1], 0, 23)
	if !ok {
		return nil, false
	}
	doms, domStar, ok := parseCronField(fields[2], 1, 31)
	if !ok {
		return nil, false
	}
	months, _, ok := parseCronField(fields[3], 1, 12)
	if !ok {
		return nil, false
	}
	dows, dowStar, ok := parseCronField(fields[4], 0, 7)
	if !ok {
		return nil, false
	}

	if dows&(1<<7) != 0 {
		dows |= 1 << 0
	}
	dows &= ^(uint64(1) << 7)

	return &cronSchedule{
		minutes:       minutes,
		hours:         hours,
		doms:          doms,
		months:        months,
		dows:          dows,
		domRestricted: !domStar,
		dowRestricted: !dowStar,
	}, true
}

func parseCronField(field string, min, max int) (bits uint64, star bool, ok bool) {
	if field == "" {
		return 0, false, false
	}
	star = true
	for _, part := range strings.Split(field, ",") {
		if part == "" {
			return 0, false, false
		}
		base := part
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			base = part[:i]
			stepStr := part[i+1:]
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return 0, false, false
			}
			step = n
		}
		if base != "*" {
			star = false
		}

		var lo, hi int
		switch {
		case base == "*":
			lo, hi = min, max
		case strings.Contains(base, "-"):
			bounds := strings.SplitN(base, "-", 2)
			loN, err1 := strconv.Atoi(bounds[0])
			hiN, err2 := strconv.Atoi(bounds[1])
			if err1 != nil || err2 != nil {
				return 0, false, false
			}
			if loN < min || hiN > max || loN > hiN {
				return 0, false, false
			}
			lo, hi = loN, hiN
		default:
			n, err := strconv.Atoi(base)
			if err != nil || n < min || n > max {
				return 0, false, false
			}
			lo, hi = n, n
			if step != 1 {
				hi = max
			}
		}

		for v := lo; v <= hi; v += step {
			bits |= uint64(1) << uint(v)
		}
	}
	return bits, star, true
}

func IsValidCron(expr string) bool {
	_, ok := parseCron(expr)
	return ok
}

func NextCronMatch(expr string, after time.Time) (time.Time, bool) {
	sched, ok := parseCron(expr)
	if !ok {
		return time.Time{}, false
	}
	candidate := after.Truncate(time.Minute).Add(time.Minute)
	horizon := after.AddDate(5, 0, 0)
	for !candidate.After(horizon) {
		if sched.matches(candidate) {
			return candidate, true
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, false
}

func (s *cronSchedule) matches(t time.Time) bool {
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
