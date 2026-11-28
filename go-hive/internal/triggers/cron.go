package triggers

import (
	"math/bits"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Field indexes mirror croniter's positions for the
// minute/hour/day/month/dow/second/year layout. A 5-field expression uses the
// first five, the 6-field form appends seconds, the 7-field form appends year.
const (
	cronFieldMinute = iota
	cronFieldHour
	cronFieldDay
	cronFieldMonth
	cronFieldDow
	cronFieldSecond
	cronFieldYear
)

var cronFieldRanges = [7][2]int{
	cronFieldMinute: {0, 59},
	cronFieldHour:   {0, 23},
	cronFieldDay:    {1, 31},
	cronFieldMonth:  {1, 12},
	cronFieldDow:    {0, 6},
	cronFieldSecond: {0, 59},
	cronFieldYear:   {1970, 2099},
}

// cronLenMeansAll is croniter's LEN_MEANS_ALL: a field that expands to every
// value of its domain collapses to "*". The day fields only collapse when the
// sibling day field contains a wildcard, which preserves Vixie day_or.
var cronLenMeansAll = [7]int{60, 24, 31, 12, 7, 60, 130}

// cronMacros is exactly croniter 6.0.0's macro table. The lookup is on the
// whole lowercased expression, so " @daily" is not a macro (croniter splits it
// into a single field and rejects it). croniter has no other macros.
var cronMacros = map[string]string{
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

var cronMonthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var cronDowNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// The patterns below are the ones croniter 6.0.0 compiles. Matching them with
// the standard library keeps the acceptance rules identical without pulling in
// a dependency.
var (
	cronStepRe      = regexp.MustCompile(`^([^-]+)-([^-/]+)(/([0-9]+))?$`)
	cronStarStepRe  = regexp.MustCompile(`^\*(/.+)$`)
	cronSlashRe     = regexp.MustCompile(`^(.+)/(.+)$`)
	cronStarOrIntRe = regexp.MustCompile(`^([0-9]+|\*)$`)
	cronDigitsRe    = regexp.MustCompile(`^[0-9]+$`)
)

// cronNthSet describes the "#n" / "l<dow>" constraints attached to one weekday.
// mask has bit n set for "nth weekday" 1..5; last means "last such weekday".
type cronNthSet struct {
	mask uint8
	last bool
}

type cronSchedule struct {
	seconds uint64
	minutes uint64
	hours   uint64
	doms    uint64
	months  uint64
	dows    uint64

	// domLast is croniter's "l" in day-of-month: the last day of the month.
	domLast bool

	// nth is non-empty when the day-of-week field used "#n" or "l<dow>".
	// croniter then matches those weekdays only and ignores day-of-month, so
	// the ordinary dow bitset is not consulted.
	nth    map[int]*cronNthSet
	hasNth bool

	domRestricted bool
	dowRestricted bool

	// years covers 1970..2099 (bit i = 1970+i), croniter's year domain.
	years   [3]uint64
	yearSet bool
}

// cronFieldExpansion is the parsed form of one field before it is packed into
// the schedule bitmasks.
type cronFieldExpansion struct {
	values    map[int]bool
	last      bool
	starToken bool
	count     int
	nth       map[int]*cronNthSet
}

func (e *cronFieldExpansion) bits(min, max int) uint64 {
	if e.starToken {
		var set uint64
		for v := min; v <= max; v++ {
			set |= uint64(1) << uint(v)
		}
		return set
	}
	var set uint64
	for v := range e.values {
		set |= uint64(1) << uint(v)
	}
	return set
}

// parseCron accepts every expression croniter 6.0.0 accepts for a trigger
// schedule: 5 fields, the 6-field second-at-the-end form (second_at_beginning
// is false for every caller in this repo) and the 7-field second+year form.
// Names, macros, L/#, ?, wrap-around ranges and year fields follow croniter's
// expansion rules exactly; Quartz extensions croniter itself rejects (W, L-n,
// 5L, bare L in day-of-week) are rejected here too. The Python fixture in
// testdata is generated from the real croniter and is the behavioural
// reference.
func parseCron(expr string) (*cronSchedule, bool) {
	lowered := strings.ToLower(expr)
	if macro, ok := cronMacros[lowered]; ok {
		lowered = macro
	}
	fields := strings.Fields(lowered)
	nfields := len(fields)
	if nfields != 5 && nfields != 6 && nfields != 7 {
		return nil, false
	}

	minuteExp, ok := expandCronField(cronFieldMinute, fields[0], nfields)
	if !ok {
		return nil, false
	}
	hourExp, ok := expandCronField(cronFieldHour, fields[1], nfields)
	if !ok {
		return nil, false
	}
	domExp, ok := expandCronField(cronFieldDay, fields[2], nfields)
	if !ok {
		return nil, false
	}
	monthExp, ok := expandCronField(cronFieldMonth, fields[3], nfields)
	if !ok {
		return nil, false
	}
	dowExp, ok := expandCronField(cronFieldDow, fields[4], nfields)
	if !ok {
		return nil, false
	}

	// A 5-field expression fires at second 0 of a matching minute; the 6- and
	// 7-field forms carry an explicit seconds field.
	secondExp := &cronFieldExpansion{values: map[int]bool{0: true}, count: 1}
	if nfields >= 6 {
		secondExp, ok = expandCronField(cronFieldSecond, fields[5], nfields)
		if !ok {
			return nil, false
		}
	}

	var yearExp *cronFieldExpansion
	if nfields == 7 {
		yearExp, ok = expandCronField(cronFieldYear, fields[6], nfields)
		if !ok {
			return nil, false
		}
	}

	// croniter rejects mixing literal weekdays with nth-weekday syntax unless
	// the DOW field expanded to the whole week.
	if len(dowExp.nth) > 0 {
		for v := range dowExp.values {
			if _, ok := dowExp.nth[v]; !ok && len(dowExp.values) != 7 {
				return nil, false
			}
		}
	}

	// croniter's day_or only ORs the day fields when neither expanded to "*".
	// A field expands to "*" when it is a bare star or when it covers its whole
	// domain and the sibling day field contains a wildcard.
	domStar := domExp.starToken || (domExp.count == cronLenMeansAll[cronFieldDay] && strings.Contains(fields[cronFieldDow], "*"))
	dowStar := dowExp.starToken || (dowExp.count == cronLenMeansAll[cronFieldDow] && strings.Contains(fields[cronFieldDay], "*"))

	sched := &cronSchedule{
		seconds:       secondExp.bits(0, 59),
		minutes:       minuteExp.bits(0, 59),
		hours:         hourExp.bits(0, 23),
		doms:          domExp.bits(1, 31),
		months:        monthExp.bits(1, 12),
		dows:          dowExp.bits(0, 6),
		domLast:       domExp.last,
		domRestricted: !domStar,
		dowRestricted: !dowStar,
	}
	if len(dowExp.nth) > 0 {
		sched.nth = dowExp.nth
		sched.hasNth = true
	}
	if yearExp != nil {
		yearStar := yearExp.starToken || yearExp.count == cronLenMeansAll[cronFieldYear]
		if !yearStar {
			sched.yearSet = true
			for v := range yearExp.values {
				idx := v - cronFieldRanges[cronFieldYear][0]
				if idx < 0 || idx >= 130 {
					return nil, false
				}
				sched.years[idx/64] |= uint64(1) << uint(idx%64)
			}
		}
	}
	return sched, true
}

// expandCronField follows croniter's per-field expansion: split on commas,
// apply croniter's DOW "#"/"l" forms, normalise "*" and "a/n", expand ranges
// (including wrap-around), and apply the value aliases that depend on the field
// count. Any value that falls outside the field's own domain is rejected, which
// is what croniter's re-injection of expanded values ends up doing.
func expandCronField(idx int, field string, nfields int) (*cronFieldExpansion, bool) {
	if strings.Contains(field, "?") {
		if field != "?" || (idx != cronFieldDay && idx != cronFieldDow) {
			return nil, false
		}
		field = "*"
	}

	exp := &cronFieldExpansion{values: map[int]bool{}, nth: map[int]*cronNthSet{}}
	for _, element := range strings.Split(field, ",") {
		if element == "" {
			return nil, false
		}

		base := element
		nth, nthLast := 0, false
		hasNth := false
		if idx == cronFieldDow {
			var ok bool
			base, nth, nthLast, hasNth, ok = splitCronDowNth(element)
			if !ok {
				return nil, false
			}
		}

		values, isLast, isStar, ok := expandCronElement(idx, base, nfields)
		if !ok {
			return nil, false
		}
		if isStar {
			exp.starToken = true
		}
		if isLast && !hasNth {
			exp.last = true
		}
		if hasNth {
			for _, v := range values {
				set := exp.nth[v]
				if set == nil {
					set = &cronNthSet{}
					exp.nth[v] = set
				}
				if nthLast {
					set.last = true
				} else {
					set.mask |= uint8(1) << uint(nth)
				}
			}
		}
		for _, v := range values {
			exp.values[v] = true
		}
	}

	exp.count = len(exp.values)
	if exp.last {
		exp.count++
	}
	return exp, true
}

// splitCronDowNth recognises croniter's two day-of-week special forms:
// "<value>#<n>" for the n-th weekday and "l<value>" for the last weekday.
// The first return value is the element croniter re-parses after stripping the
// suffix. ok is false only for syntactically special forms with an invalid
// ordinal; tokens that merely do not match are passed through unchanged so the
// normal parser rejects them the same way croniter does.
func splitCronDowNth(element string) (base string, nth int, last bool, hasNth bool, ok bool) {
	if len(element) > 1 && element[0] == 'l' && cronDigitsRe.MatchString(element[1:]) {
		return element[1:], 0, true, true, true
	}
	hash := strings.IndexByte(element, '#')
	if hash < 0 {
		return element, 0, false, false, true
	}
	he := element[:hash]
	ordinal := element[hash+1:]
	if !cronDigitsRe.MatchString(ordinal) || !cronDowHeValid(he) {
		return element, 0, false, false, true
	}
	n, err := strconv.Atoi(ordinal)
	if err != nil || n < 1 || n > 5 {
		return "", 0, false, false, false
	}
	return he, n, false, true, true
}

// cronDowHeValid mirrors the "he" branch of croniter's special_dow_re: a word,
// or a range whose two ends are both weekday names or both month names. Month
// ranges are matched by croniter's regex but fail its DOW name lookup later.
func cronDowHeValid(he string) bool {
	if he == "" {
		return false
	}
	word := true
	for i := 0; i < len(he); i++ {
		c := he[i]
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '_' {
			word = false
			break
		}
	}
	if word {
		return true
	}
	if strings.Count(he, "-") != 1 {
		return false
	}
	parts := strings.SplitN(he, "-", 2)
	if _, ok := cronDowNames[parts[0]]; ok {
		_, ok = cronDowNames[parts[1]]
		return ok
	}
	if _, ok := cronMonthNames[parts[0]]; ok {
		_, ok = cronMonthNames[parts[1]]
		return ok
	}
	return false
}

// expandCronElement parses one comma-separated element. isLast reports a bare
// day-of-month "l"; isStar reports a bare "*" token.
func expandCronElement(idx int, element string, nfields int) (values []int, isLast, isStar, ok bool) {
	min, max := cronFieldRanges[idx][0], cronFieldRanges[idx][1]

	t := element
	if m := cronStarStepRe.FindStringSubmatch(t); m != nil {
		t = strconv.Itoa(min) + "-" + strconv.Itoa(max) + m[1]
	}
	m := cronStepRe.FindStringSubmatch(t)
	if m == nil {
		// croniter rewrites "a/n" to "a-MAX/n" before trying the range regex.
		if sm := cronSlashRe.FindStringSubmatch(element); sm != nil {
			t = sm[1] + "-" + strconv.Itoa(max) + "/" + sm[2]
		} else {
			t = element
		}
		m = cronStepRe.FindStringSubmatch(t)
	}

	if m != nil {
		lowStr, highStr, stepStr := m[1], m[2], m[4]
		// croniter special-cases the upper bound "l" of a day-of-month range
		// to 31, so "1-L" is the whole month. A lower bound of "l" is rejected
		// by its band check.
		if idx == cronFieldDay && highStr == "l" {
			highStr = "31"
		}
		low, okLow := cronRangeBand(idx, lowStr)
		high, okHigh := cronRangeBand(idx, highStr)
		if !okLow || !okHigh {
			return nil, false, false, false
		}
		step := 1
		if stepStr != "" {
			n, err := strconv.Atoi(stepStr)
			if err != nil || n <= 0 {
				return nil, false, false, false
			}
			step = n
		}
		low = cronValueAlias(low, idx, nfields)
		high = cronValueAlias(high, idx, nfields)
		if cronMaxInt(low, high) > max {
			return nil, false, false, false
		}

		var rng []int
		switch {
		case low > high:
			// Wrapped range: low..MAX then MIN..high, respecting the step the
			// way croniter does (including its step-skip adjustment).
			rng = cronSeq(low, max, step)
			toSkip := 0
			if len(rng) > 0 {
				lastVal := rng[len(rng)-1]
				alreadySkipped := max - lastVal
				curpos := lastVal - min
				if curpos+step > (max-min+1) && alreadySkipped < step {
					toSkip = step - alreadySkipped
				}
			}
			rng = append(rng, cronSeq(min+toSkip, high, step)...)
		case low == high:
			// croniter quirk: equal bounds mean the whole field cycle
			// (e.g. "Sun-Sun" or "5/1" at the field maximum).
			rng = cronSeq(min, max, step)
		default:
			rng = cronSeq(low, high, step)
		}
		for _, v := range rng {
			// croniter re-injects expanded values and bounds-checks each one;
			// this rejects ranges that reach below the field minimum.
			if v < min || v > max {
				return nil, false, false, false
			}
			values = append(values, v)
		}
		return values, false, false, true
	}

	if strings.HasPrefix(t, "-") {
		return nil, false, false, false
	}
	if !cronStarOrIntRe.MatchString(t) {
		v, isL, ok := cronAlphaConv(idx, t)
		if !ok {
			return nil, false, false, false
		}
		if isL {
			return nil, true, false, true
		}
		v = cronValueAlias(v, idx, nfields)
		if v < min || v > max {
			return nil, false, false, false
		}
		return []int{v}, false, false, true
	}
	if t == "*" {
		return nil, false, true, true
	}
	n, err := strconv.Atoi(t)
	if err != nil {
		return nil, false, false, false
	}
	n = cronValueAlias(n, idx, nfields)
	if n < min || n > max {
		return nil, false, false, false
	}
	return []int{n}, false, false, true
}

// cronRangeBand resolves one bound of a range. croniter's band check runs
// before name conversion, so a non-numeric band that is not a valid name (or is
// day-of-month "l", only valid as a standalone token) is rejected.
func cronRangeBand(idx int, band string) (int, bool) {
	if cronDigitsRe.MatchString(band) {
		n, err := strconv.Atoi(band)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	v, isLast, ok := cronAlphaConv(idx, band)
	if !ok || isLast {
		return 0, false
	}
	return v, true
}

// cronAlphaConv mirrors croniter's ALPHACONV table. isLast marks day-of-month
// "l"; no other field accepts an alphabetic token.
func cronAlphaConv(idx int, token string) (value int, isLast, ok bool) {
	switch idx {
	case cronFieldDay:
		if token == "l" {
			return 0, true, true
		}
	case cronFieldMonth:
		if v, found := cronMonthNames[token]; found {
			return v, false, true
		}
	case cronFieldDow:
		if v, found := cronDowNames[token]; found {
			return v, false, true
		}
	}
	return 0, false, false
}

// cronValueAlias mirrors croniter's LOWMAP, whose aliases are disabled for some
// field/field-count combinations: day-of-month 0 aliases to 1 only in the
// 6-field form, and day-of-week 7 aliases to Sunday only in the 5-field form.
// Month 0 is never aliased.
func cronValueAlias(v, idx, nfields int) int {
	if idx == cronFieldDay && v == 0 && nfields == 6 {
		return 1
	}
	if idx == cronFieldDow && v == 7 && nfields == 5 {
		return 0
	}
	return v
}

func cronSeq(lo, hi, step int) []int {
	if lo > hi || step <= 0 {
		return nil
	}
	out := make([]int, 0, (hi-lo)/step+1)
	for v := lo; v <= hi; v += step {
		out = append(out, v)
	}
	return out
}

func cronMaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
	location := after.Location()

	// Croniter has no small look-ahead horizon: a schedule may legitimately
	// skip years (Feb 29 at a century boundary is 8 years apart). The
	// Gregorian calendar repeats every 400 years, so any expression that can
	// ever match does so within one full cycle. Year-restricted expressions
	// cannot match outside croniter's 1970..2099 year domain.
	startYear := candidate.Year()
	lastYear := startYear + 400
	if s.yearSet && lastYear > cronFieldRanges[cronFieldYear][1] {
		lastYear = cronFieldRanges[cronFieldYear][1]
	}
	for year := startYear; year <= lastYear; year++ {
		if s.yearSet && !s.yearSelected(year) {
			continue
		}
		firstMonth := 1
		if year == startYear {
			firstMonth = int(candidate.Month())
		}
		for month := firstMonth; month <= 12; month++ {
			if s.months&(uint64(1)<<uint(month)) == 0 {
				continue
			}
			lastDay := cronDaysInMonth(year, time.Month(month))
			firstDay := 1
			if year == startYear && month == int(candidate.Month()) {
				firstDay = candidate.Day()
			}
			for day := firstDay; day <= lastDay; day++ {
				dayStart := time.Date(year, time.Month(month), day, 0, 0, 0, 0, location)
				if !s.matchesDay(dayStart) {
					continue
				}
				firstHour, firstMinute, firstSecond := 0, 0, 0
				if year == startYear && month == int(candidate.Month()) && day == candidate.Day() {
					firstHour, firstMinute, firstSecond = candidate.Hour(), candidate.Minute(), candidate.Second()
				}
				for hour := firstHour; hour < 24; hour++ {
					if s.hours&(uint64(1)<<uint(hour)) == 0 {
						continue
					}
					minuteStart := 0
					if hour == firstHour {
						minuteStart = firstMinute
					}
					for minute := minuteStart; minute < 60; minute++ {
						if s.minutes&(uint64(1)<<uint(minute)) == 0 {
							continue
						}
						secondStart := 0
						if hour == firstHour && minute == firstMinute {
							secondStart = firstSecond
						}
						for second := secondStart; second < 60; second++ {
							if s.seconds&(uint64(1)<<uint(second)) == 0 {
								continue
							}
							return time.Date(year, time.Month(month), day, hour, minute, second, 0, location), true
						}
					}
				}
			}
		}
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

// matchesDay is the date-level part of a match: year, month, day-of-month and
// day-of-week with croniter's day_or rule. When the expression used "#n" or
// "l<dow>", croniter matches the nth weekday and ignores day-of-month, so the
// same rule applies here.
func (s *cronSchedule) matchesDay(t time.Time) bool {
	if s.yearSet && !s.yearSelected(t.Year()) {
		return false
	}
	if s.months&(uint64(1)<<uint(int(t.Month()))) == 0 {
		return false
	}
	if s.hasNth {
		nthMatch := s.matchesNthWeekday(t)
		if s.dowRestricted || !s.domRestricted {
			// When the DOW field stayed a literal list, croniter unions the two
			// day interpretations, and the day-of-month-only branch can never
			// be earlier than the nth-weekday branch, so the nth weekday wins.
			return nthMatch
		}
		// The DOW field expanded to "*" (its seven values collapsed because
		// day-of-month contains a wildcard). croniter skips its day_or union
		// and intersects the day-of-month with the nth weekday.
		return s.matchesDayOfMonth(t) && nthMatch
	}
	domMatch := s.matchesDayOfMonth(t)
	dowMatch := s.dows&(uint64(1)<<uint(int(t.Weekday()))) != 0
	if s.domRestricted && s.dowRestricted {
		return domMatch || dowMatch
	}
	return domMatch && dowMatch
}

func (s *cronSchedule) matchesDayOfMonth(t time.Time) bool {
	if s.doms&(uint64(1)<<uint(t.Day())) != 0 {
		return true
	}
	return s.domLast && t.Day() == cronDaysInMonth(t.Year(), t.Month())
}

func (s *cronSchedule) matchesNthWeekday(t time.Time) bool {
	set, ok := s.nth[int(t.Weekday())]
	if !ok {
		return false
	}
	day := t.Day()
	occurrence := (day-1)/7 + 1
	if occurrence <= 5 && set.mask&(uint8(1)<<uint(occurrence)) != 0 {
		return true
	}
	if set.last {
		return day+7 > cronDaysInMonth(t.Year(), t.Month())
	}
	return false
}

func (s *cronSchedule) yearSelected(year int) bool {
	idx := year - cronFieldRanges[cronFieldYear][0]
	if idx < 0 || idx >= 130 {
		return false
	}
	return s.years[idx/64]&(uint64(1)<<uint(idx%64)) != 0
}

func cronDaysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func (s *cronSchedule) matchesMinute(t time.Time) bool {
	if s.minutes&(uint64(1)<<uint(t.Minute())) == 0 {
		return false
	}
	if s.hours&(uint64(1)<<uint(t.Hour())) == 0 {
		return false
	}
	return s.matchesDay(t)
}

// countBetween returns how many schedule ticks fall in [lo, hi] on one calendar
// day. Counting bitmasks instead of walking every minute keeps a multi-year
// catch-up cheap while still matching croniter tick for tick.
func (s *cronSchedule) countBetween(lo, hi time.Time) int {
	if hi.Before(lo) {
		return 0
	}
	if lo.Hour() == 0 && lo.Minute() == 0 && lo.Second() == 0 &&
		hi.Hour() == 23 && hi.Minute() == 59 && hi.Second() == 59 {
		return bits.OnesCount64(s.hours) * bits.OnesCount64(s.minutes) * bits.OnesCount64(s.seconds)
	}
	count := 0
	for hour := lo.Hour(); hour <= hi.Hour(); hour++ {
		if s.hours&(uint64(1)<<uint(hour)) == 0 {
			continue
		}
		minuteStart, minuteEnd := 0, 59
		if hour == lo.Hour() {
			minuteStart = lo.Minute()
		}
		if hour == hi.Hour() {
			minuteEnd = hi.Minute()
		}
		for minute := minuteStart; minute <= minuteEnd; minute++ {
			if s.minutes&(uint64(1)<<uint(minute)) == 0 {
				continue
			}
			secondStart, secondEnd := 0, 59
			if hour == lo.Hour() && minute == lo.Minute() {
				secondStart = lo.Second()
			}
			if hour == hi.Hour() && minute == hi.Minute() {
				secondEnd = hi.Second()
			}
			if secondStart > secondEnd {
				continue
			}
			count += bits.OnesCount64(s.seconds & secondRangeMask(secondStart, secondEnd))
		}
	}
	return count
}

// appendTicks appends the matching timestamps in [lo, hi] until the report cap
// is reached, in chronological order.
func (s *cronSchedule) appendTicks(lo, hi time.Time, ticks *[]string) {
	for hour := lo.Hour(); hour <= hi.Hour() && len(*ticks) < MaxReportedTicks; hour++ {
		if s.hours&(uint64(1)<<uint(hour)) == 0 {
			continue
		}
		minuteStart, minuteEnd := 0, 59
		if hour == lo.Hour() {
			minuteStart = lo.Minute()
		}
		if hour == hi.Hour() {
			minuteEnd = hi.Minute()
		}
		for minute := minuteStart; minute <= minuteEnd && len(*ticks) < MaxReportedTicks; minute++ {
			if s.minutes&(uint64(1)<<uint(minute)) == 0 {
				continue
			}
			secondStart, secondEnd := 0, 59
			if hour == lo.Hour() && minute == lo.Minute() {
				secondStart = lo.Second()
			}
			if hour == hi.Hour() && minute == hi.Minute() {
				secondEnd = hi.Second()
			}
			for second := secondStart; second <= secondEnd && len(*ticks) < MaxReportedTicks; second++ {
				if s.seconds&(uint64(1)<<uint(second)) == 0 {
					continue
				}
				tick := time.Date(lo.Year(), lo.Month(), lo.Day(), hour, minute, second, 0, lo.Location())
				*ticks = append(*ticks, formatISO(tick))
			}
		}
	}
}

func secondRangeMask(lo, hi int) uint64 {
	low := uint64(1)<<uint(lo) - 1
	high := uint64(1)<<uint(hi+1) - 1
	return high &^ low
}
