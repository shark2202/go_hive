//go:build ignore

// perfrecall15k: recall/search performance harness for the 15,000-turn store.
//
// Deliberately outside the product build: the `ignore` build tag keeps this
// file out of `go list ./...`, `go vet ./...` and the architecture graph
// (`gen_arch.py`). Build it by naming the file (file-list mode ignores build
// constraints):
//
//	CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
//	  go build -o /tmp/perfrecall e2e/perfrecall15k/main.go
//
// Subcommands:
//
//	gen  -home DIR -turns N -seed S
//	     Generate N synthetic turns across three queen scopes under DIR:
//	     one events.jsonl per session (user message, streamed assistant
//	     snapshots, tool results — the exact shapes SearchSessionMessages
//	     reads) plus a timeline.jsonl per session for search_timeline.
//
//	bench -home DIR -json OUT [-cpuprofile P]
//	     Measure the recall index cold build (direct + first-query path),
//	     warm recall queries, index load/parse cost, search_messages /
//	     search_timeline latency and this process's peak RSS.
//
//	servebench -binary PATH -home DIR -label NAME -runs N -json OUT
//	     Measure `hive serve` time-to-health and RSS for one home.
//
// Companion runner: go_hive/scripts/perf_recall_15k.sh.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/memory"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: perfrecall15k <gen|bench|servebench> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "gen":
		runGen(os.Args[2:])
	case "bench":
		runBench(os.Args[2:])
	case "servebench":
		runServeBench(os.Args[2:])
	case "report":
		runReport(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}

func msSince(t time.Time) float64 {
	return float64(time.Since(t).Microseconds()) / 1000.0
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "perfrecall15k: "+format+"\n", args...)
	os.Exit(1)
}

// ── synthetic corpus ─────────────────────────────────────────────────

var enTopics = []string{
	"the Postgres migration",
	"the refund policy",
	"the Kubernetes rollout",
	"the billing invoice flow",
	"the customer onboarding checklist",
	"the search relevance tuning",
	"the hotel booking integration",
	"the latency regression",
	"the data retention audit",
	"the vendor renewal",
	"the warehouse sync job",
	"the mobile push campaign",
	"the API rate limit",
	"the quarterly budget review",
	"the incident postmortem",
}

var zhTopics = []string{
	"数据库迁移", "退款政策", "灰度发布", "发票流程", "获客活动",
	"留存分析", "接口限流", "季度预算", "事故复盘", "数据保留策略",
}

var enAspects = []string{
	"error rate", "latency", "cost", "coverage", "rollback plan",
	"dashboard", "pipeline", "backfill", "quarterly numbers", "vendor quote",
}

var zhAspects = []string{
	"迁移计划", "超时问题", "验收标准", "成本", "监控面板",
	"回滚方案", "数据校验", "上线节奏", "预算", "复盘结论",
}

func pick(rng *rand.Rand, options []string) string {
	return options[rng.Intn(len(options))]
}

// topicFor keeps the first few topics hot (a realistic Zipf-ish head) so BM25
// queries have non-trivial document frequencies to score against.
func topicFor(rng *rand.Rand, zh bool) string {
	if zh {
		if rng.Intn(10) < 6 {
			return zhTopics[rng.Intn(4)]
		}
		return zhTopics[rng.Intn(len(zhTopics))]
	}
	if rng.Intn(10) < 6 {
		return enTopics[rng.Intn(4)]
	}
	return enTopics[rng.Intn(len(enTopics))]
}

func userText(rng *rand.Rand, topic string, zh bool) string {
	if zh {
		switch rng.Intn(4) {
		case 0:
			return fmt.Sprintf("帮我看一下%s，%s 这块还是没定下来，周五前要一个结论。", topic, pick(rng, zhAspects))
		case 1:
			return fmt.Sprintf("%s现在是什么状态？上次说的%s有没有进展？", topic, pick(rng, zhAspects))
		case 2:
			return fmt.Sprintf("把%s相关的记录都找出来，下周开会要用，重点是%s。", topic, pick(rng, zhAspects))
		default:
			return fmt.Sprintf("我想重新看一下%s，之前%s的数据对不上，麻烦帮我核一遍。", topic, pick(rng, zhAspects))
		}
	}
	switch rng.Intn(5) {
	case 0:
		return fmt.Sprintf("Can you look into %s? The %s looks off in staging and I need the numbers before the review.", topic, pick(rng, enAspects))
	case 1:
		return fmt.Sprintf("I want to revisit %s from last week — what did we decide about the %s?", topic, pick(rng, enAspects))
	case 2:
		return fmt.Sprintf("Please pull everything we know about %s. The %s asked for a summary by Friday.", topic, pick(rng, enAspects))
	case 3:
		return fmt.Sprintf("Remind me what broke with %s. The last fix landed but the %s still looks wrong.", topic, pick(rng, enAspects))
	default:
		return fmt.Sprintf("What is the current state of %s? Last time the %s number did not add up.", topic, pick(rng, enAspects))
	}
}

func replyText(rng *rand.Rand, topic string, zh bool, turn int) string {
	ok := 20 + rng.Intn(400)
	total := ok + rng.Intn(40)
	failed := total - ok
	slow := 0.4 + rng.Float64()*9
	if zh {
		return fmt.Sprintf("我查了%s 的记录（第 %d 轮）。一共 %d 条里成功 %d 条、失败 %d 条，最慢的一步是 %s，用了 %.1f 秒。"+
			"我把细节写进当天的备注里了，明天按%s的说法重跑一次，有问题我再叫你。",
			topic, turn, total, ok, failed, pick(rng, zhAspects), slow, pick(rng, zhAspects))
	}
	return fmt.Sprintf("I checked %s (turn %d). %d of %d runs succeeded and %d failed; the slowest step is %s at %.1f seconds. "+
		"I wrote the details into the notes and scheduled a re-run for tomorrow; ping me if the %s still disagrees.",
		topic, turn, ok, total, failed, pick(rng, enAspects), slow, pick(rng, enAspects))
}

// toolResultText mirrors what a real tool result looks like on disk: a JSON
// body, occasionally a multi-kilobyte payload.
func toolResultText(rng *rand.Rand, topic string, big bool) string {
	rows := 3 + rng.Intn(500)
	took := 15 + rng.Intn(9000)
	body := fmt.Sprintf(`{"tool":"query","topic":%q,"rows":%d,"took_ms":%d,"status":"ok","summary":"scanned %d records for %s"}`,
		topic, rows, took, rows*3, topic)
	if big {
		var b strings.Builder
		b.WriteString(body)
		b.WriteString("\n")
		filler := fmt.Sprintf("record=%s source=synthetic-store state=ok ", strings.ReplaceAll(strings.TrimPrefix(topic, "the "), " ", "-"))
		for b.Len() < 4096+rng.Intn(8)<<10 {
			b.WriteString(filler)
		}
		body = b.String()
	}
	return body
}

// ── gen ──────────────────────────────────────────────────────────────

type queenStat struct {
	Owner         string `json:"owner"`
	Turns         int    `json:"turns"`
	Sessions      int    `json:"sessions"`
	EventLines    int    `json:"event_lines"`
	EventBytes    int64  `json:"event_bytes"`
	TimelineLines int    `json:"timeline_lines"`
}

type genSummary struct {
	Home          string      `json:"home"`
	Seed          int64       `json:"seed"`
	Turns         int         `json:"turns"`
	Sessions      int         `json:"sessions"`
	EventLines    int         `json:"event_lines"`
	TimelineItems int         `json:"timeline_items"`
	EventBytes    int64       `json:"event_bytes"`
	GenMs         float64     `json:"gen_ms"`
	Queens        []queenStat `json:"queens"`
}

type event struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Data      any    `json:"data"`
}

type inputData struct {
	Content string `json:"content"`
}

type deltaData struct {
	Snapshot  string `json:"snapshot"`
	Iteration int    `json:"iteration"`
	InnerTurn int    `json:"inner_turn"`
}

type toolData struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
	Result    string `json:"result"`
}

type toolStartData struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
}

type reasoningData struct {
	Delta string `json:"delta"`
}

type timelineEntry struct {
	Kind    string   `json:"kind"`
	Text    string   `json:"text"`
	Start   string   `json:"start"`
	End     string   `json:"end"`
	Turn    int      `json:"turn"`
	Aliases []string `json:"aliases"`
}

func runGen(args []string) {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	home := fs.String("home", "", "HIVE_HOME root to generate into")
	turns := fs.Int("turns", 15000, "total conversation turns to generate")
	seed := fs.Int64("seed", 20261122, "deterministic RNG seed")
	jsonOut := fs.String("json", "", "write the generation summary as JSON here")
	_ = fs.Parse(args)
	if *home == "" || *turns < 10 {
		fatalf("gen: -home and -turns (>=10) are required")
	}
	started := time.Now()
	rng := rand.New(rand.NewSource(*seed))

	a := *turns * 2 / 5 // 40%
	b := *turns / 3     // ~33%
	c := *turns - a - b
	plans := []struct {
		owner string
		turns int
	}{{"queen_alpha", a}, {"queen_beta", b}, {"queen_gamma", c}}

	summary := genSummary{Home: *home, Seed: *seed, Turns: *turns}
	cursor := time.Date(2025, 6, 1, 9, 0, 0, 0, time.Local)
	for qi, plan := range plans {
		stat, next := genQueen(*home, rng, plan.owner, byte('a'+qi), plan.turns, cursor)
		cursor = next
		summary.Queens = append(summary.Queens, stat)
		summary.Sessions += stat.Sessions
		summary.EventLines += stat.EventLines
		summary.TimelineItems += stat.TimelineLines
		summary.EventBytes += stat.EventBytes
	}
	summary.GenMs = msSince(started)
	printGenSummary(summary)
	if *jsonOut != "" {
		writeJSONFile(*jsonOut, summary)
	}
}

// genQueen writes one queen's sessions and returns its stats plus the next
// session cursor position.
func genQueen(home string, rng *rand.Rand, owner string, suffixByte byte, turns int, cursor time.Time) (queenStat, time.Time) {
	stat := queenStat{Owner: owner, Turns: turns}
	sessions := turns / 50
	if sessions < 2 {
		sessions = 2
	}
	stat.Sessions = sessions
	perSession := distributeTurns(rng, sessions, turns)

	root := filepath.Join(home, "queens", owner, "sessions")
	if err := os.MkdirAll(root, 0o755); err != nil {
		fatalf("gen: mkdir %s: %v", root, err)
	}

	for si, sessionTurns := range perSession {
		cursor = cursor.Add(2*time.Hour + time.Duration(rng.Intn(18))*time.Hour)
		sessionID := fmt.Sprintf("session_%s_%c%03d", cursor.Format("20060102_150405"), suffixByte, si)
		dir := filepath.Join(root, sessionID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fatalf("gen: mkdir %s: %v", dir, err)
		}
		eventsFile, err := os.Create(filepath.Join(dir, "events.jsonl"))
		if err != nil {
			fatalf("gen: create events.jsonl: %v", err)
		}
		w := bufio.NewWriterSize(eventsFile, 1<<20)
		turnTime := cursor
		for ti := 0; ti < sessionTurns; ti++ {
			turnTime = turnTime.Add(time.Duration(1+rng.Intn(7)) * time.Minute)
			zh := rng.Intn(100) < 15
			topic := topicFor(rng, zh)
			lines := writeTurn(w, rng, sessionID, ti, turnTime, topic, zh)
			stat.EventLines += lines
		}
		if err := w.Flush(); err != nil {
			fatalf("gen: flush events.jsonl: %v", err)
		}
		if err := eventsFile.Close(); err != nil {
			fatalf("gen: close events.jsonl: %v", err)
		}
		if info, err := os.Stat(filepath.Join(dir, "events.jsonl")); err == nil {
			stat.EventBytes += info.Size()
		}

		items := 2 + rng.Intn(3)
		timelineFile, err := os.Create(filepath.Join(dir, "timeline.jsonl"))
		if err != nil {
			fatalf("gen: create timeline.jsonl: %v", err)
		}
		tw := bufio.NewWriter(timelineFile)
		for i := 0; i < items; i++ {
			zh := rng.Intn(100) < 15
			topic := topicFor(rng, zh)
			day := cursor.AddDate(0, 0, -rng.Intn(14))
			kind := []string{"event", "fact", "plan"}[i%3]
			entry := timelineEntry{
				Kind:    kind,
				Text:    fmt.Sprintf("The user discussed %s with the assistant.", topic),
				Start:   day.Format("2006-01-02"),
				End:     day.AddDate(0, 0, rng.Intn(3)).Format("2006-01-02"),
				Turn:    rng.Intn(sessionTurns) + 1,
				Aliases: []string{topic, pick(rng, enAspects)},
			}
			raw, _ := json.Marshal(entry)
			tw.Write(raw)
			tw.WriteByte('\n')
			stat.TimelineLines++
		}
		tw.Flush()
		timelineFile.Close()
	}
	return stat, cursor
}

func distributeTurns(rng *rand.Rand, sessions, turns int) []int {
	out := make([]int, sessions)
	base := turns / sessions
	sum := 0
	for i := range out {
		out[i] = base + rng.Intn(21) - 10
		if out[i] < 5 {
			out[i] = 5
		}
		sum += out[i]
	}
	for diff := turns - sum; diff != 0; {
		i := rng.Intn(sessions)
		if diff > 0 {
			out[i]++
			diff--
		} else if out[i] > 5 {
			out[i]--
			diff++
		}
	}
	return out
}

// writeTurn appends one conversation turn to the events stream and returns
// the number of event lines written. Shape mirrors a real transcript: user
// message, reasoning deltas, streamed cumulative assistant snapshots (the
// last write wins in SearchSessionMessages), tool start/result events, and
// for ~30% of turns a second tool result plus a follow-up assistant stream.
func writeTurn(w *bufio.Writer, rng *rand.Rand, sessionID string, turn int, ts time.Time, topic string, zh bool) int {
	lines := 0
	write := func(typ string, at time.Time, data any) {
		raw, err := json.Marshal(event{Type: typ, Timestamp: at.Format("2006-01-02T15:04:05"), Data: data})
		if err != nil {
			fatalf("gen: marshal event: %v", err)
		}
		w.Write(raw)
		w.WriteByte('\n')
		lines++
	}
	writeStream := func(full string, innerTurn int, start time.Time) {
		runes := []rune(full)
		chunks := 5 + rng.Intn(6) // 5..10 cumulative snapshots per reply
		for i := 1; i <= chunks; i++ {
			upto := len(runes) * i / chunks
			write("client_output_delta", start.Add(time.Duration(600*i)*time.Millisecond), deltaData{
				Snapshot:  string(runes[:upto]),
				Iteration: 0,
				InnerTurn: innerTurn,
			})
		}
	}
	full := replyText(rng, topic, zh, turn)
	write("client_input_received", ts, inputData{Content: userText(rng, topic, zh)})
	write("reasoning_delta", ts.Add(500*time.Millisecond), reasoningData{
		Delta: fmt.Sprintf("The user is asking about %s; checking the past sessions.", topic),
	})
	writeStream(full, 0, ts.Add(time.Second))
	toolID := fmt.Sprintf("call_%s_t%d", sessionID, turn)
	write("tool_call_started", ts.Add(7*time.Second), toolStartData{ToolName: "query", ToolUseID: toolID})
	write("tool_call_completed", ts.Add(8*time.Second), toolData{
		ToolName:  "query",
		ToolUseID: toolID,
		Result:    toolResultText(rng, topic, rng.Intn(20) == 0),
	})
	if rng.Intn(10) < 3 {
		write("reasoning_delta", ts.Add(9*time.Second), reasoningData{
			Delta: fmt.Sprintf("Cross-checking %s against the earlier summary.", topic),
		})
		secondID := fmt.Sprintf("call_%s_t%d_b", sessionID, turn)
		write("tool_call_started", ts.Add(9*time.Second), toolStartData{ToolName: "search", ToolUseID: secondID})
		write("tool_call_completed", ts.Add(10*time.Second), toolData{
			ToolName:  "search",
			ToolUseID: secondID,
			Result:    toolResultText(rng, topic, false),
		})
		writeStream(fmt.Sprintf("Follow-up on %s: the %s check now passes.", topic, pick(rng, enAspects)), 1, ts.Add(11*time.Second))
	}
	return lines
}

func printGenSummary(s genSummary) {
	fmt.Printf("gen: home=%s turns=%d sessions=%d event_lines=%d timeline_items=%d event_bytes=%d (%.1f MB) in %.1f ms\n",
		s.Home, s.Turns, s.Sessions, s.EventLines, s.TimelineItems, s.EventBytes, float64(s.EventBytes)/(1<<20), s.GenMs)
	for _, q := range s.Queens {
		fmt.Printf("gen:   %-12s turns=%5d sessions=%3d event_lines=%6d event_bytes=%9d timeline=%d\n",
			q.Owner, q.Turns, q.Sessions, q.EventLines, q.EventBytes, q.TimelineLines)
	}
}

// ── latency stats ────────────────────────────────────────────────────

type latencyStats struct {
	N    int       `json:"n"`
	P50  float64   `json:"p50_ms"`
	P95  float64   `json:"p95_ms"`
	P99  float64   `json:"p99_ms"`
	Min  float64   `json:"min_ms"`
	Max  float64   `json:"max_ms"`
	Mean float64   `json:"mean_ms"`
	Raw  []float64 `json:"raw_ms"`
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := p * float64(len(sorted)-1)
	lo := int(rank)
	if lo >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	frac := rank - float64(lo)
	return sorted[lo]*(1-frac) + sorted[lo+1]*frac
}

func summarize(durs []float64) latencyStats {
	s := latencyStats{N: len(durs), Raw: append([]float64(nil), durs...)}
	if len(durs) == 0 {
		return s
	}
	sorted := append([]float64(nil), durs...)
	sort.Float64s(sorted)
	s.Min = sorted[0]
	s.Max = sorted[len(sorted)-1]
	sum := 0.0
	for _, d := range sorted {
		sum += d
	}
	s.Mean = sum / float64(len(sorted))
	s.P50 = pct(sorted, 0.50)
	s.P95 = pct(sorted, 0.95)
	s.P99 = pct(sorted, 0.99)
	return s
}

func (s latencyStats) String() string {
	return fmt.Sprintf("n=%d p50=%.1fms p95=%.1fms p99=%.1fms min=%.1fms max=%.1fms mean=%.1fms",
		s.N, s.P50, s.P95, s.P99, s.Min, s.Max, s.Mean)
}

// ── bench ────────────────────────────────────────────────────────────

var benchQueries = []string{
	"postgres migration",
	"refund policy",
	"kubernetes rollout",
	"billing invoice",
	"onboarding checklist",
	"search relevance",
	"hotel booking",
	"latency regression",
	"data retention audit",
	"vendor renewal",
	"warehouse sync",
	"push campaign",
	"rate limit",
	"budget review",
	"incident postmortem",
	"deployment rollback",
	"数据库迁移",
	"退款政策",
	"发票流程",
	"事故复盘",
}

var searchMessagePatterns = []string{
	"(?i)postgres|kubernetes",
	"(?i)refund policy",
	"zzz_no_such_token_xyz",
}

var searchTimelinePatterns = []string{
	"kubernetes|postgres|迁移|退款",
	"rollout|migration|发票",
	"zzz_no_such_token_xyz",
}

type scopeBuild struct {
	Owner   string  `json:"owner"`
	BuildMs float64 `json:"build_ms"`
	Err     string  `json:"err,omitempty"`
}

type indexFile struct {
	Scope string `json:"scope"`
	Owner string `json:"owner"`
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	Turns int64  `json:"turns"`
}

type coldFirstQuery struct {
	Scope             string  `json:"scope"`
	Query             string  `json:"query"`
	FirstQueryMs      float64 `json:"first_query_ms"`
	FirstQueryResults int     `json:"first_query_results"`
	IndexAvailableMs  float64 `json:"index_available_ms"`
	PostBuildQueryMs  float64 `json:"post_build_query_ms"`
	PostBuildResults  int     `json:"post_build_results"`
}

type warmStats struct {
	Queries  []string `json:"queries"`
	Runs     int      `json:"runs"`
	NonEmpty int      `json:"non_empty_results"`
	latencyStats
}

type scopeWarm struct {
	Owner string `json:"owner"`
	Turns int64  `json:"indexed_turns"`
	latencyStats
}

type searchBench struct {
	Scope   string `json:"scope"`
	Owner   string `json:"owner"`
	Pattern string `json:"pattern"`
	Runs    int    `json:"runs"`
	Matches int    `json:"matches_last_run"`
	latencyStats
}

type memStats struct {
	PeakRSSBytes            int64  `json:"peak_rss_bytes"`
	HeapAllocAfterWarmBytes uint64 `json:"heap_alloc_after_warm_bytes"`
	HeapSysBytes            uint64 `json:"heap_sys_bytes"`
	NumGC                   uint32 `json:"num_gc"`
}

type benchDoc struct {
	Home               string         `json:"home"`
	GoVersion          string         `json:"go_version"`
	GOMAXPROCS         int            `json:"gomaxprocs"`
	StartedAt          string         `json:"started_at"`
	HostLoadBefore     string         `json:"host_load_before"`
	HostLoadAfterWarm  string         `json:"host_load_after_warm"`
	DirectBuild        []scopeBuild   `json:"direct_build"`
	DirectBuildTotalMs float64        `json:"direct_build_total_ms"`
	IndexFiles         []indexFile    `json:"index_files"`
	ColdFirstQuery     coldFirstQuery `json:"cold_first_query"`
	WarmQueries        warmStats      `json:"warm_queries"`
	WarmByScope        []scopeWarm    `json:"warm_by_scope"`
	IndexLoadParse     latencyStats   `json:"index_load_parse_replica"`
	SearchMessages     []searchBench  `json:"search_messages"`
	SearchMessagesAll  latencyStats   `json:"search_messages_overall"`
	SearchTimeline     []searchBench  `json:"search_timeline"`
	SearchTimelineAll  latencyStats   `json:"search_timeline_overall"`
	Mem                memStats       `json:"mem"`
	Notes              []string       `json:"notes"`
}

type mirrorTurn struct {
	Session string `json:"session"`
	Ordinal int    `json:"ordinal"`
	User    string `json:"user"`
	Reply   string `json:"reply"`
}

type mirrorSession struct {
	EventsSize int64 `json:"events_size"`
}

type mirrorIndex struct {
	Version  int                      `json:"version"`
	Sessions map[string]mirrorSession `json:"sessions"`
	Turns    []mirrorTurn             `json:"turns"`
}

// indexTurns counts the turns inside an index file without keeping it.
func indexTurns(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var doc mirrorIndex
	if err := json.NewDecoder(f).Decode(&doc); err != nil {
		return 0, err
	}
	return int64(len(doc.Turns)), nil
}

func runBench(args []string) {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	home := fs.String("home", "", "generated HIVE_HOME root")
	jsonOut := fs.String("json", "", "write the benchmark document as JSON here")
	cpuprofile := fs.String("cpuprofile", "", "write a CPU profile of 20 extra warm queries here")
	_ = fs.Parse(args)
	if *home == "" {
		fatalf("bench: -home is required")
	}

	doc := benchDoc{
		Home:       *home,
		GoVersion:  runtime.Version(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		StartedAt:  time.Now().Format(time.RFC3339),
	}
	doc.HostLoadBefore = hostLoadAvg()

	refs := []memory.ScopeRef{
		{Scope: memory.SearchScopeQueens, Owner: "queen_alpha"},
		{Scope: memory.SearchScopeQueens, Owner: "queen_beta"},
		{Scope: memory.SearchScopeQueens, Owner: "queen_gamma"},
	}
	alpha := refs[0]
	indexRoot := filepath.Join(*home, ".message_index")

	// 1. Direct cold build per scope (the upgrade rebuild).
	if err := os.RemoveAll(indexRoot); err != nil {
		fatalf("bench: reset index root: %v", err)
	}
	for _, ref := range refs {
		t0 := time.Now()
		err := memory.BuildRecallIndex(*home, ref)
		entry := scopeBuild{Owner: ref.Owner, BuildMs: msSince(t0)}
		if err != nil {
			entry.Err = err.Error()
		}
		doc.DirectBuild = append(doc.DirectBuild, entry)
		doc.DirectBuildTotalMs += entry.BuildMs
		fmt.Printf("bench: direct build %-12s %8.1f ms %s\n", ref.Owner, entry.BuildMs, entry.Err)
	}
	for _, ref := range refs {
		path := memory.RecallIndexPath(*home, ref)
		info, err := os.Stat(path)
		if err != nil {
			fatalf("bench: stat index %s: %v", path, err)
		}
		turns, _ := indexTurns(path)
		doc.IndexFiles = append(doc.IndexFiles, indexFile{
			Scope: string(ref.Scope), Owner: ref.Owner, Path: path, Bytes: info.Size(), Turns: turns,
		})
		fmt.Printf("bench: index %-12s %9d bytes turns=%d\n", ref.Owner, info.Size(), turns)
	}

	// 2. First query after an upgrade: the index is absent, recall triggers a
	// background build and waits at most its deadline (3s by default).
	if err := os.RemoveAll(indexRoot); err != nil {
		fatalf("bench: reset index root: %v", err)
	}
	indexPath := memory.RecallIndexPath(*home, alpha)
	t0 := time.Now()
	available := make(chan float64, 1)
	go func() {
		for {
			if _, err := os.Stat(indexPath); err == nil {
				available <- msSince(t0)
				return
			}
			if time.Since(t0) > 180*time.Second {
				available <- -1
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	query := "postgres migration refund policy"
	firstTurns := memory.RecallPastConversations(*home, alpha, query, memory.PastRecallOptions{})
	doc.ColdFirstQuery = coldFirstQuery{
		Scope:             "queens/queen_alpha",
		Query:             query,
		FirstQueryMs:      msSince(t0),
		FirstQueryResults: len(firstTurns),
	}
	doc.ColdFirstQuery.IndexAvailableMs = <-available
	// Once the build has landed, the next query reads the index.
	deadline := time.Now().Add(180 * time.Second)
	for {
		if _, err := os.Stat(indexPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			fatalf("bench: index for %s never appeared", alpha.Owner)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t1 := time.Now()
	postTurns := memory.RecallPastConversations(*home, alpha, query, memory.PastRecallOptions{})
	doc.ColdFirstQuery.PostBuildQueryMs = msSince(t1)
	doc.ColdFirstQuery.PostBuildResults = len(postTurns)
	fmt.Printf("bench: cold first query %.1f ms (results=%d, index available at %.1f ms), post-build query %.1f ms (results=%d)\n",
		doc.ColdFirstQuery.FirstQueryMs, doc.ColdFirstQuery.FirstQueryResults,
		doc.ColdFirstQuery.IndexAvailableMs, doc.ColdFirstQuery.PostBuildQueryMs, doc.ColdFirstQuery.PostBuildResults)

	// 3. Warm recall queries: 120 measured runs over 20 queries.
	for i := 0; i < 2; i++ {
		memory.RecallPastConversations(*home, alpha, benchQueries[i], memory.PastRecallOptions{})
	}
	var warmDurs []float64
	nonEmpty := 0
	for i := 0; i < 120; i++ {
		q := benchQueries[i%len(benchQueries)]
		t := time.Now()
		turns := memory.RecallPastConversations(*home, alpha, q, memory.PastRecallOptions{})
		warmDurs = append(warmDurs, msSince(t))
		if len(turns) > 0 {
			nonEmpty++
		}
	}
	doc.WarmQueries = warmStats{Queries: benchQueries, Runs: len(warmDurs), NonEmpty: nonEmpty, latencyStats: summarize(warmDurs)}
	doc.HostLoadAfterWarm = hostLoadAvg()
	fmt.Printf("bench: warm recall queries (%s scope) %s non_empty=%d/120 (load %s -> %s)\n",
		alpha.Owner, doc.WarmQueries.latencyStats, nonEmpty, doc.HostLoadBefore, doc.HostLoadAfterWarm)

	// 3b. Cross-scope warm check: 20 runs per scope, so the per-query cost can
	// be read against each scope's indexed turn count.
	for _, ref := range refs {
		if _, err := os.Stat(memory.RecallIndexPath(*home, ref)); err != nil {
			if err := memory.BuildRecallIndex(*home, ref); err != nil {
				fatalf("bench: build %s index: %v", ref.Owner, err)
			}
		}
	}
	for _, ref := range refs {
		entry := scopeWarm{Owner: ref.Owner}
		if turns, err := indexTurns(memory.RecallIndexPath(*home, ref)); err == nil {
			entry.Turns = turns
		}
		var durs []float64
		for i := 0; i < 20; i++ {
			q := benchQueries[i%len(benchQueries)]
			t := time.Now()
			memory.RecallPastConversations(*home, ref, q, memory.PastRecallOptions{})
			durs = append(durs, msSince(t))
		}
		entry.latencyStats = summarize(durs)
		doc.WarmByScope = append(doc.WarmByScope, entry)
		fmt.Printf("bench: warm by scope %-12s turns=%5d %s\n", ref.Owner, entry.Turns, entry.latencyStats)
	}

	// 4. Index load+parse replica: what one recall query pays just to read the
	// derived file (same decoder the runtime uses: json.Decoder).
	var loadDurs []float64
	for i := 0; i < 20; i++ {
		t := time.Now()
		f, err := os.Open(indexPath)
		if err != nil {
			fatalf("bench: open index: %v", err)
		}
		var doc mirrorIndex
		if err := json.NewDecoder(f).Decode(&doc); err != nil {
			f.Close()
			fatalf("bench: decode index: %v", err)
		}
		f.Close()
		loadDurs = append(loadDurs, msSince(t))
	}
	doc.IndexLoadParse = summarize(loadDurs)
	fmt.Printf("bench: index load+parse replica %s\n", doc.IndexLoadParse)

	// 5. search_messages / search_timeline over every queen scope.
	var msgDurs, tlDurs []float64
	for _, ref := range refs {
		for _, pattern := range searchMessagePatterns {
			bench := searchBench{Scope: string(ref.Scope), Owner: ref.Owner, Pattern: pattern}
			for i := 0; i < 8; i++ {
				t := time.Now()
				res, err := memory.SearchMessages(*home, ref, memory.SearchMessagesOptions{Pattern: pattern, MaxMatches: 20})
				bench.rawAppend(msSince(t))
				if err != nil {
					fatalf("bench: search_messages %s: %v", ref.Owner, err)
				}
				bench.Matches = res.TotalMatches
			}
			bench.Runs = len(bench.Raw)
			bench.latencyStats = summarize(bench.Raw)
			bench.Raw = bench.latencyStats.Raw
			doc.SearchMessages = append(doc.SearchMessages, bench)
			msgDurs = append(msgDurs, bench.Raw...)
			fmt.Printf("bench: search_messages %-12s %-24s %s matches=%d\n", ref.Owner, pattern, bench.latencyStats, bench.Matches)
		}
		for _, pattern := range searchTimelinePatterns {
			bench := searchBench{Scope: string(ref.Scope), Owner: ref.Owner, Pattern: pattern}
			for i := 0; i < 8; i++ {
				t := time.Now()
				res, err := memory.SearchTimeline(*home, ref, memory.SearchTimelineOptions{Pattern: pattern, MaxResults: 60})
				bench.rawAppend(msSince(t))
				if err != nil {
					fatalf("bench: search_timeline %s: %v", ref.Owner, err)
				}
				bench.Matches = res.Total
			}
			bench.Runs = len(bench.Raw)
			bench.latencyStats = summarize(bench.Raw)
			bench.Raw = bench.latencyStats.Raw
			doc.SearchTimeline = append(doc.SearchTimeline, bench)
			tlDurs = append(tlDurs, bench.Raw...)
			fmt.Printf("bench: search_timeline %-12s %-24s %s total=%d\n", ref.Owner, pattern, bench.latencyStats, bench.Matches)
		}
	}
	doc.SearchMessagesAll = summarize(msgDurs)
	doc.SearchTimelineAll = summarize(tlDurs)

	// 6. Optional CPU profile over extra warm queries for bottleneck
	// attribution (timings above stay unprofiled).
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			fatalf("bench: create cpuprofile: %v", err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			fatalf("bench: start cpuprofile: %v", err)
		}
		for i := 0; i < 20; i++ {
			memory.RecallPastConversations(*home, alpha, benchQueries[i%len(benchQueries)], memory.PastRecallOptions{})
		}
		pprof.StopCPUProfile()
		f.Close()
		fmt.Printf("bench: cpu profile (20 warm queries) -> %s\n", *cpuprofile)
	}

	// 7. Leave every scope's index on disk so the evidence listing is
	// complete: the first-query step above only rebuilt alpha's.
	for _, ref := range refs {
		path := memory.RecallIndexPath(*home, ref)
		if _, err := os.Stat(path); err != nil {
			if err := memory.BuildRecallIndex(*home, ref); err != nil {
				fatalf("bench: rebuild %s index: %v", ref.Owner, err)
			}
		}
	}

	// 8. Memory footprint of this process.
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err == nil {
		doc.Mem.PeakRSSBytes = rssBytesFromRusage(ru)
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	doc.Mem.HeapAllocAfterWarmBytes = ms.HeapAlloc
	doc.Mem.HeapSysBytes = ms.Sys
	doc.Mem.NumGC = ms.NumGC
	fmt.Printf("bench: peak RSS=%d bytes heap_alloc=%d bytes sys=%d bytes n_gc=%d\n",
		doc.Mem.PeakRSSBytes, doc.Mem.HeapAllocAfterWarmBytes, doc.Mem.HeapSysBytes, doc.Mem.NumGC)

	doc.Notes = append(doc.Notes,
		"cold build = BuildRecallIndex per scope, invoked directly; the first-query row is the real upgrade path (background build + 3s wait cap)",
		"warm queries = full RecallPastConversations, index present; each call re-reads and re-parses the index file then re-tokenizes every indexed turn",
		"index_load_parse_replica = os.Open + json.Decoder of the same file, 20 runs (the file read every warm query pays)",
		"peak RSS = getrusage of this bench process only (data generation ran in a separate process)")
	if *jsonOut != "" {
		writeJSONFile(*jsonOut, doc)
	}
}

// rawAppend records one duration before the final summarize() replaces the
// latencyStats fields (Raw is the only field it reads at that point).
func (b *searchBench) rawAppend(d float64) {
	b.Raw = append(b.Raw, d)
}

func rssBytesFromRusage(ru syscall.Rusage) int64 {
	// Darwin reports bytes in Maxrss, Linux kilobytes.
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) * 1024
}

// hostLoadAvg records the host load so timing variance on a shared machine can
// be attributed (macOS: "{ 2.34 3.12 4.56 }").
func hostLoadAvg() string {
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func writeJSONFile(path string, value any) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fatalf("marshal json: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		fatalf("write %s: %v", path, err)
	}
}

// ── servebench ───────────────────────────────────────────────────────

type serveRun struct {
	Label        string  `json:"label"`
	Run          int     `json:"run"`
	Port         int     `json:"port"`
	ReadyMs      float64 `json:"ready_ms"`
	RSSReadyKB   int64   `json:"rss_ready_kb"`
	RSSAfter2sKB int64   `json:"rss_after_2s_kb"`
	Log          string  `json:"log"`
	Err          string  `json:"err,omitempty"`
}

type serveDoc struct {
	Binary   string     `json:"binary"`
	Home     string     `json:"home"`
	Label    string     `json:"label"`
	WarmupMs float64    `json:"warmup_ms,omitempty"`
	Runs     []serveRun `json:"runs"`
	MedianMs float64    `json:"median_ready_ms"`
}

func runServeBench(args []string) {
	fs := flag.NewFlagSet("servebench", flag.ExitOnError)
	binary := fs.String("binary", "", "path to the hive binary")
	home := fs.String("home", "", "HIVE_HOME for serve")
	label := fs.String("label", "", "label for the JSON (e.g. empty / 15k)")
	runs := fs.Int("runs", 3, "number of timed runs")
	warmups := fs.Int("warmup", 1, "untimed warm-up runs (first-ever boot migration/cache costs)")
	port0 := fs.Int("port0", 8810, "first port; run N uses port0+N")
	logDir := fs.String("logdir", "", "directory for per-run server logs")
	jsonOut := fs.String("json", "", "write the serve benchmark document as JSON here")
	_ = fs.Parse(args)
	if *binary == "" || *home == "" {
		fatalf("servebench: -binary and -home are required")
	}
	if *logDir == "" {
		*logDir = os.TempDir()
	}
	doc := serveDoc{Binary: *binary, Home: *home, Label: *label}
	client := &http.Client{Timeout: time.Second}
	for run := 0; run < *warmups; run++ {
		record := runServeOnce(client, *binary, *home, *label, -1-run, *port0+90+run, *logDir)
		doc.WarmupMs = record.ReadyMs
		fmt.Printf("servebench: %-6s warmup=%d port=%d ready=%8.1fms (excluded from median)\n",
			*label, run, record.Port, record.ReadyMs)
	}
	var ready []float64
	for run := 0; run < *runs; run++ {
		record := runServeOnce(client, *binary, *home, *label, run, *port0+run, *logDir)
		doc.Runs = append(doc.Runs, record)
		if record.Err == "" {
			ready = append(ready, record.ReadyMs)
		}
		fmt.Printf("servebench: %-6s run=%d port=%d ready=%8.1fms rss_ready=%6dkB rss_2s=%6dkB err=%q\n",
			*label, run, record.Port, record.ReadyMs, record.RSSReadyKB, record.RSSAfter2sKB, record.Err)
		time.Sleep(500 * time.Millisecond)
	}
	doc.MedianMs = median(ready)
	fmt.Printf("servebench: %-6s median ready %.1f ms over %d runs\n", *label, doc.MedianMs, len(ready))
	if *jsonOut != "" {
		writeJSONFile(*jsonOut, doc)
	}
}

// runServeOnce starts `hive serve` on one port, waits for /api/health to
// return 200, records RSS, then stops it.
func runServeOnce(client *http.Client, binary, home, label string, run, port int, logDir string) serveRun {
	record := serveRun{Label: label, Run: run, Port: port}
	logPath := filepath.Join(logDir, fmt.Sprintf("serve-%s-run%d.txt", label, run))
	record.Log = logPath
	logFile, err := os.Create(logPath)
	if err != nil {
		fatalf("servebench: create log: %v", err)
	}
	cmd := exec.Command(binary, "serve", "-p", strconv.Itoa(port))
	cmd.Env = envWith(os.Environ(), "HIVE_HOME="+home, "HIVE_NO_FILE_LOG=1")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		fatalf("servebench: start: %v", err)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/api/health", port)
	readyOK := false
	for time.Since(t0) < 90*time.Second {
		resp, err := client.Get(url)
		if err == nil {
			if resp.StatusCode == 200 {
				resp.Body.Close()
				readyOK = true
				break
			}
			resp.Body.Close()
		}
		time.Sleep(20 * time.Millisecond)
	}
	record.ReadyMs = msSince(t0)
	if !readyOK {
		record.Err = "health never returned 200 within 90s"
	} else {
		record.RSSReadyKB = processRSSKB(cmd.Process.Pid)
		time.Sleep(2 * time.Second)
		record.RSSAfter2sKB = processRSSKB(cmd.Process.Pid)
	}
	// Stop the server: SIGTERM, wait, then SIGKILL.
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(12 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
	logFile.Close()
	return record
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func processRSSKB(pid int) int64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return -1
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return -1
	}
	return value
}

// envWith returns base with each "KEY=value" override replacing any existing
// entry for KEY (Go's own runtime lets the last duplicate win; libc getenv
// would take the first, so duplicates must not be passed through).
func envWith(base []string, overrides ...string) []string {
	keys := map[string]struct{}{}
	for _, override := range overrides {
		keys[strings.SplitN(override, "=", 2)[0]] = struct{}{}
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		if _, replaced := keys[strings.SplitN(entry, "=", 2)[0]]; replaced {
			continue
		}
		out = append(out, entry)
	}
	return append(out, overrides...)
}

// ── report ───────────────────────────────────────────────────────────

// runReport renders summary.txt from the machine-readable evidence files; the
// numbers in the doc come from here, so they are recomputable from the JSON.
func runReport(args []string) {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	benchPath := fs.String("bench", "", "bench.json")
	genPath := fs.String("gen", "", "gen.json")
	serveEmptyPath := fs.String("serve-empty", "", "serve JSON for the empty home")
	serve15kPath := fs.String("serve-15k", "", "serve JSON for the 15k home")
	out := fs.String("out", "", "write the summary here")
	_ = fs.Parse(args)

	var bench benchDoc
	readJSONFile(*benchPath, &bench)
	var gen genSummary
	if *genPath != "" {
		readJSONFile(*genPath, &gen)
	}
	var empty, fifteen serveDoc
	if *serveEmptyPath != "" {
		readJSONFile(*serveEmptyPath, &empty)
	}
	if *serve15kPath != "" {
		readJSONFile(*serve15kPath, &fifteen)
	}

	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	p("perf-recall-15k summary (all numbers from bench.json / serve-*.json / gen.json)")
	p("")
	if gen.Turns > 0 {
		p("data: turns=%d sessions=%d event_lines=%d timeline_items=%d event_bytes=%d (%.1f MB)",
			gen.Turns, gen.Sessions, gen.EventLines, gen.TimelineItems, gen.EventBytes, float64(gen.EventBytes)/(1<<20))
	}
	var indexBytes int64
	var indexTurnsTotal int64
	for _, f := range bench.IndexFiles {
		indexBytes += f.Bytes
		indexTurnsTotal += f.Turns
	}
	p("index: files=%d turns=%d bytes=%d (%.2f MB)", len(bench.IndexFiles), indexTurnsTotal, indexBytes, float64(indexBytes)/(1<<20))
	p("")
	p("cold build (direct BuildRecallIndex):")
	for _, entry := range bench.DirectBuild {
		p("  %-12s %8.1f ms %s", entry.Owner, entry.BuildMs, entry.Err)
	}
	p("  total        %8.1f ms", bench.DirectBuildTotalMs)
	p("first query after upgrade (index absent, 3s wait cap):")
	p("  first_query_ms=%.1f results=%d index_available_ms=%.1f post_build_query_ms=%.1f post_build_results=%d",
		bench.ColdFirstQuery.FirstQueryMs, bench.ColdFirstQuery.FirstQueryResults,
		bench.ColdFirstQuery.IndexAvailableMs, bench.ColdFirstQuery.PostBuildQueryMs, bench.ColdFirstQuery.PostBuildResults)
	p("")
	p("warm recall queries: %s (non_empty=%d/%d)", bench.WarmQueries.latencyStats, bench.WarmQueries.NonEmpty, bench.WarmQueries.Runs)
	for _, scope := range bench.WarmByScope {
		p("  by scope %-12s turns=%5d %s", scope.Owner, scope.Turns, scope.latencyStats)
	}
	p("index load+parse replica: %s", bench.IndexLoadParse)
	p("search_messages overall: %s", bench.SearchMessagesAll)
	p("search_timeline overall: %s", bench.SearchTimelineAll)
	p("bench process peak RSS: %d bytes (%.0f MB), heap_alloc=%d bytes",
		bench.Mem.PeakRSSBytes, float64(bench.Mem.PeakRSSBytes)/(1<<20), bench.Mem.HeapAllocAfterWarmBytes)
	p("host load (vm.loadavg): before=%s after_warm=%s", bench.HostLoadBefore, bench.HostLoadAfterWarm)
	p("")
	if len(empty.Runs) > 0 || len(fifteen.Runs) > 0 {
		p("serve cold start (time to /api/health 200):")
		p("  %-6s median=%8.1f ms warmup=%.1f ms runs=%s", empty.Label, empty.MedianMs, empty.WarmupMs, formatServeRuns(empty.Runs))
		p("  %-6s median=%8.1f ms warmup=%.1f ms runs=%s", fifteen.Label, fifteen.MedianMs, fifteen.WarmupMs, formatServeRuns(fifteen.Runs))
		p("  rss_ready: empty=%dkB 15k=%dkB (ps rss of the serve process)",
			firstRSS(empty.Runs), firstRSS(fifteen.Runs))
	}
	p("")
	p("against the upstream v1.0.15 claims:")
	p("  cold build ~19 s / 15,000 turns -> measured %.0f ms total (%.2fx faster)", bench.DirectBuildTotalMs, ratioFloat(19000, bench.DirectBuildTotalMs))
	p("  warm recall query ~30 ms        -> measured p50=%.1f ms p95=%.1f ms p99=%.1f ms", bench.WarmQueries.P50, bench.WarmQueries.P95, bench.WarmQueries.P99)
	p("  recall limit 3 s (first query)  -> measured first_query_ms=%.1f (index ready at %.1f ms)", bench.ColdFirstQuery.FirstQueryMs, bench.ColdFirstQuery.IndexAvailableMs)
	p("  index file size                 -> %.2f MB for %d turns across %d scopes", float64(indexBytes)/(1<<20), indexTurnsTotal, len(bench.IndexFiles))

	summary := b.String()
	fmt.Print(summary)
	if *out != "" {
		if err := os.WriteFile(*out, []byte(summary), 0o644); err != nil {
			fatalf("report: write %s: %v", *out, err)
		}
	}
}

func ratioFloat(claim, measured float64) float64 {
	if measured <= 0 {
		return 0
	}
	return claim / measured
}

func formatServeRuns(runs []serveRun) string {
	parts := make([]string, 0, len(runs))
	for _, run := range runs {
		parts = append(parts, fmt.Sprintf("%.1fms", run.ReadyMs))
	}
	return strings.Join(parts, ", ")
}

func firstRSS(runs []serveRun) int64 {
	if len(runs) == 0 {
		return -1
	}
	return runs[0].RSSReadyKB
}

func readJSONFile(path string, value any) {
	data, err := os.ReadFile(path)
	if err != nil {
		fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		fatalf("parse %s: %v", path, err)
	}
}
