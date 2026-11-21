package triggers

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const MaxReportedTicks = 100

type TriggerDefinition struct {
	ID            string         `json:"id"`
	TriggerType   string         `json:"trigger_type"`
	TriggerConfig map[string]any `json:"trigger_config"`
	Description   string         `json:"description"`
	Task          string         `json:"task"`
	Enabled       bool           `json:"enabled"`
	LastFiredAt   *string        `json:"last_fired_at"`
	NextDueAt     *string        `json:"next_due_at"`
}

type MissedTrigger struct {
	TriggerID   string   `json:"trigger_id"`
	TriggerType string   `json:"trigger_type"`
	Count       int      `json:"count"`
	Ticks       []string `json:"ticks"`
	NextDueAt   *string  `json:"next_due_at"`
}

func (m MissedTrigger) ToDict() map[string]any {
	ticks := make([]string, len(m.Ticks))
	copy(ticks, m.Ticks)
	return map[string]any{
		"trigger_id":   m.TriggerID,
		"trigger_type": m.TriggerType,
		"count":        m.Count,
		"ticks":        ticks,
		"next_due_at":  m.NextDueAt,
	}
}

func ParseISO(ts *string) *time.Time {
	if ts == nil || *ts == "" {
		return nil
	}
	s := strings.ReplaceAll(*ts, "Z", "+00:00")
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if dt, err := time.Parse(layout, s); err == nil {
			utc := dt.UTC()
			return &utc
		}
	}
	return nil
}

func formatISO(t time.Time) string {
	t = t.UTC()
	base := t.Format("2006-01-02T15:04:05")
	if us := t.Nanosecond() / 1000; us != 0 {
		base = fmt.Sprintf("%s.%06d", base, us)
	}
	return base + "+00:00"
}

func FormatISO(t time.Time) string {
	return formatISO(t)
}

func isoPtr(t time.Time) *string {
	s := formatISO(t)
	return &s
}

// Parity: core/framework/host/triggers.py::_missed_for_cron
func missedForCron(cronExpr string, since, now time.Time) (int, []string, *string) {
	sched, ok := parseCron(cronExpr)
	if !ok {
		return 0, []string{}, nil
	}

	// Mirror croniter: ticks are strictly after `since` and at-or-before
	// `now`, at second resolution. A 6-field schedule can fire several times
	// within one matching minute, so each matching minute contributes every
	// matching second instead of a single tick.
	start := since.Truncate(time.Second).Add(time.Second)
	lastMinute := now.Truncate(time.Minute)
	horizonMinute := since.AddDate(cronHorizonYears, 0, 0).Truncate(time.Minute)
	if horizonMinute.Before(lastMinute) {
		lastMinute = horizonMinute
	}

	ticks := []string{}
	count := 0
	minute := start.Truncate(time.Minute)
	firstSecond := start.Second()
	for !minute.After(lastMinute) {
		if sched.matchesMinute(minute) {
			lastSecond := 59
			if minute.Equal(now.Truncate(time.Minute)) {
				lastSecond = now.Second()
			}
			for sec := firstSecond; sec <= lastSecond; sec++ {
				if sched.seconds&(uint64(1)<<uint(sec)) == 0 {
					continue
				}
				count++
				if len(ticks) < MaxReportedTicks {
					ticks = append(ticks, formatISO(minute.Add(time.Duration(sec)*time.Second)))
				}
			}
		}
		minute = minute.Add(time.Minute)
		firstSecond = 0
	}

	var nextDue *string
	if next, ok := sched.next(now); ok {
		nextDue = isoPtr(next)
	}
	return count, ticks, nextDue
}

func missedForInterval(intervalMinutes float64, since, now time.Time) (int, []string, *string) {
	if intervalMinutes <= 0 {
		return 0, []string{}, nil
	}
	step := time.Duration(intervalMinutes * float64(time.Minute))
	if step <= 0 {
		return 0, []string{}, nil
	}
	if !now.After(since) {
		return 0, []string{}, isoPtr(since.Add(step))
	}
	elapsed := now.Sub(since)
	count := int(float64(elapsed) / float64(step))
	if count <= 0 {
		return 0, []string{}, isoPtr(since.Add(step))
	}

	ticks := []string{}
	cursor := since.Add(step)
	for !cursor.After(now) && len(ticks) < MaxReportedTicks {
		ticks = append(ticks, formatISO(cursor))
		cursor = cursor.Add(step)
	}
	nextDue := isoPtr(since.Add(step * time.Duration(count+1)))
	return count, ticks, nextDue
}

func ComputeMissed(triggers []map[string]any, now time.Time) []MissedTrigger {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	out := []MissedTrigger{}
	for _, entry := range triggers {
		if !truthy(entry["enabled"]) {
			continue
		}
		ttype := strVal(entry["trigger_type"])
		if ttype != "timer" {
			continue
		}

		last := ParseISO(stringPtr(entry["last_fired_at"]))
		if last == nil {
			continue
		}

		cfg := mapVal(entry["trigger_config"])
		cronExpr := strVal(cfg["cron"])
		interval := cfg["interval_minutes"]

		var (
			count   int
			ticks   []string
			nextDue *string
		)
		if cronExpr != "" {
			count, ticks, nextDue = missedForCron(cronExpr, *last, now)
		} else if truthy(interval) {
			minutes, ok := toFloat(interval)
			if !ok {
				continue
			}
			count, ticks, nextDue = missedForInterval(minutes, *last, now)
		} else {
			continue
		}

		if count == 0 {
			continue
		}

		out = append(out, MissedTrigger{
			TriggerID:   strVal(entry["id"]),
			TriggerType: ttype,
			Count:       count,
			Ticks:       ticks,
			NextDueAt:   nextDue,
		})
	}
	return out
}

type TriggerFireStat struct {
	FireCount   int
	LastFiredAt *int64
}

type TriggerViewSession interface {
	AvailableTriggers() []TriggerDefinition
	ActiveTriggerIDs() map[string]struct{}
	TriggerNextFire() map[string]float64
	TriggerFireStats() map[string]TriggerFireStat
}

type triggerViewClock interface {
	NowMonotonic() float64
	NowWall() float64
}

type TriggerView struct {
	TriggerID     string         `json:"trigger_id"`
	TriggerType   string         `json:"trigger_type"`
	TriggerConfig map[string]any `json:"trigger_config"`
	Name          string         `json:"name"`
	Task          string         `json:"task"`
	Enabled       bool           `json:"enabled"`
	LastFiredAt   *string        `json:"last_fired_at"`
	NextDueAt     *string        `json:"next_due_at"`
}

func BuildTriggerView(session TriggerViewSession) []TriggerView {
	available := session.AvailableTriggers()
	if len(available) == 0 {
		return []TriggerView{}
	}
	active := session.ActiveTriggerIDs()
	fireTimes := session.TriggerNextFire()
	fireStats := session.TriggerFireStats()

	var nowMono, nowWall float64
	if c, ok := session.(triggerViewClock); ok {
		nowMono, nowWall = c.NowMonotonic(), c.NowWall()
	} else {
		now := time.Now()
		second := float64(time.Second)
		nowMono = float64(now.UnixNano()) / second
		nowWall = nowMono
	}

	out := make([]TriggerView, 0, len(available))
	for _, t := range available {
		configOut := make(map[string]any, len(t.TriggerConfig)+3)
		for k, v := range t.TriggerConfig {
			configOut[k] = v
		}
		if mono, ok := fireTimes[t.ID]; ok {
			remaining := mono - nowMono
			if remaining < 0 {
				remaining = 0
			}
			configOut["next_fire_in"] = remaining
			configOut["next_fire_at"] = int64((nowWall + remaining) * 1000)
		}
		if stats, ok := fireStats[t.ID]; ok {
			configOut["fire_count"] = stats.FireCount
			if stats.LastFiredAt != nil {
				configOut["last_fired_at"] = *stats.LastFiredAt
			}
		}
		name := t.Description
		if name == "" {
			name = t.ID
		}
		_, enabled := active[t.ID]
		out = append(out, TriggerView{
			TriggerID:     t.ID,
			TriggerType:   t.TriggerType,
			TriggerConfig: configOut,
			Name:          name,
			Task:          t.Task,
			Enabled:       enabled,
			LastFiredAt:   t.LastFiredAt,
			NextDueAt:     t.NextDueAt,
		})
	}
	return out
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case float32:
		return x != 0
	case int:
		return x != 0
	case int8:
		return x != 0
	case int16:
		return x != 0
	case int32:
		return x != 0
	case int64:
		return x != 0
	case uint:
		return x != 0
	case uint8:
		return x != 0
	case uint16:
		return x != 0
	case uint32:
		return x != 0
	case uint64:
		return x != 0
	default:
		return true
	}
}

func strVal(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func stringPtr(v any) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

func mapVal(v any) map[string]any {
	if m, ok := v.(map[string]any); ok && m != nil {
		return m
	}
	return map[string]any{}
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err == nil {
			return f, true
		}
		return 0, false
	default:
		return 0, false
	}
}
