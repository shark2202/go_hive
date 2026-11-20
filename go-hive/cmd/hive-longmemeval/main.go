// Command hive-longmemeval runs LongMemEval (S or M) through go_hive's real
// queen session machinery and grades the replies with the benchmark's own
// per-type judge prompts.
//
// It is the Go port of core/tests/e2e/longmemeval.py. Dataset files are never
// embedded: point --data / HIVE_LME_DATA at the cleaned release (downloaded
// on first use) or at a split directory.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aden-hive/hive-go/internal/cli"
	"github.com/aden-hive/hive-go/internal/longmemeval"
)

const usage = `hive-longmemeval — LongMemEval (S or M) end to end through go_hive

usage:
  hive-longmemeval run [flags]
  hive-longmemeval rejudge <run-dir> [flags]
  hive-longmemeval reference <oracle|full-history> --out <dir> [flags]
  hive-longmemeval summary <run-dir>

Run flags:
  --data PATH          raw release file, or a split directory (default $HIVE_LME_DATA
                       or ./.cache/longmemeval)
  --cache-dir DIR      where split question files are written
  --dataset s|m        LongMemEval variant (default $HIVE_LME_DATASET or "s")
  --questions SPEC     "all", comma-separated ids, or a count (default $HIVE_LME_QUESTIONS or 12)
  --resume             skip questions already in the run directory (default $HIVE_LME_RESUME)
  --run-dir DIR        where results go (default $HIVE_LME_RUN_DIR or <data>/runs/<timestamp>)
  --queen NAME         queen to ask (default $HIVE_LME_QUEEN or queen_technology)
  --model NAME         queen model (default $HIVE_LME_MODEL / $HIVE_LIVE_MODEL)
  --api-base URL       queen endpoint (default $HIVE_LME_API_BASE / $HIVE_LIVE_API_BASE)
  --api-key KEY        queen key (prefer $HIVE_LME_API_KEY / $HIVE_LIVE_API_KEY)
  --judge-model NAME   judge model (default $HIVE_LME_JUDGE_MODEL or ` + longmemeval.OfficialJudgeModel + `)
  --judge-api-base URL judge endpoint (default $HIVE_LME_JUDGE_API_BASE or https://api.openai.com/v1)
  --judge-api-key KEY  judge key (prefer $HIVE_LME_JUDGE_API_KEY / $OPENAI_API_KEY)
  --timeout SECONDS    per-question queen turn timeout (default $HIVE_LIVE_TIMEOUT or 420)
  --keep-homes         keep each question's seeded Hive home
  --download           download the raw release when missing (default true)
  --verbose            log progress to stderr

Rejudge/reference flags: --data, --cache-dir, --dataset, --judge-model,
--judge-api-base, --judge-api-key, --concurrency (default 8), --verbose.
Reference also takes --out (required), --questions, --reader-model,
--reader-api-base, --reader-api-key.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:]))
}

func run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "run":
		return cmdRun(ctx, args[1:])
	case "rejudge":
		return cmdRejudge(ctx, args[1:])
	case "reference":
		return cmdReference(ctx, args[1:])
	case "summary":
		return cmdSummary(ctx, args[1:])
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

type commonFlags struct {
	dataPath   string
	cacheDir   string
	dataset    string
	download   bool
	verbose    bool
	judgeKey   string
	judgeBase  string
	judgeModel string
}

func (c *commonFlags) bindDataset(fs *flag.FlagSet) {
	fs.StringVar(&c.dataPath, "data", "", "raw release file or split directory")
	fs.StringVar(&c.cacheDir, "cache-dir", "", "split-file cache directory")
	fs.StringVar(&c.dataset, "dataset", envOr("HIVE_LME_DATASET", longmemeval.VariantS), "LongMemEval variant: s or m")
	fs.BoolVar(&c.download, "download", true, "download the raw release when missing")
	fs.BoolVar(&c.verbose, "verbose", false, "log progress to stderr")
}

func (c *commonFlags) bindJudge(fs *flag.FlagSet) {
	fs.StringVar(&c.judgeModel, "judge-model", envOr("HIVE_LME_JUDGE_MODEL", longmemeval.OfficialJudgeModel), "judge model")
	fs.StringVar(&c.judgeBase, "judge-api-base", envOr("HIVE_LME_JUDGE_API_BASE", "https://api.openai.com/v1"), "judge endpoint")
	fs.StringVar(&c.judgeKey, "judge-api-key", "", "judge key (prefer HIVE_LME_JUDGE_API_KEY / OPENAI_API_KEY)")
}

func (c *commonFlags) splitDir(ctx context.Context) (string, error) {
	logf := func(string, ...any) {}
	if c.verbose {
		logf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}
	return longmemeval.Prepare(ctx, longmemeval.SourceConfig{
		Variant:  c.dataset,
		DataPath: c.dataPath,
		CacheDir: c.cacheDir,
		Download: c.download,
		Logf:     logf,
	})
}

func (c *commonFlags) judge() (*longmemeval.Judge, error) {
	key := firstNonEmpty(c.judgeKey, os.Getenv("HIVE_LME_JUDGE_API_KEY"), os.Getenv("OPENAI_API_KEY"))
	client := longmemeval.NewAPIClient(c.judgeBase, key, &http.Client{Timeout: 300 * time.Second})
	if strings.TrimSpace(c.judgeModel) == "" {
		return nil, fmt.Errorf("judge model is empty")
	}
	return longmemeval.NewJudge(client, c.judgeModel), nil
}

func cmdRun(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	common := &commonFlags{}
	common.bindDataset(fs)
	common.bindJudge(fs)
	questions := fs.String("questions", envOr("HIVE_LME_QUESTIONS", "12"), "all, comma-separated ids, or a count")
	resumeDefault := os.Getenv("HIVE_LME_RESUME") == "1"
	resume := fs.Bool("resume", resumeDefault, "skip questions already recorded in the run directory")
	runDir := fs.String("run-dir", os.Getenv("HIVE_LME_RUN_DIR"), "results directory")
	queen := fs.String("queen", envOr("HIVE_LME_QUEEN", "queen_technology"), "queen to ask")
	model := fs.String("model", firstNonEmpty(os.Getenv("HIVE_LME_MODEL"), os.Getenv("HIVE_LIVE_MODEL")), "queen model")
	apiBase := fs.String("api-base", firstNonEmpty(os.Getenv("HIVE_LME_API_BASE"), os.Getenv("HIVE_LIVE_API_BASE")), "queen endpoint")
	apiKey := fs.String("api-key", "", "queen key (prefer environment)")
	timeout := fs.Float64("timeout", envFloatOr("HIVE_LIVE_TIMEOUT", 420), "per-question queen turn timeout in seconds")
	keepHomes := fs.Bool("keep-homes", false, "keep each question's seeded Hive home")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	splitDir, err := common.splitDir(ctx)
	if err != nil {
		return fail(err)
	}
	judge, err := common.judge()
	if err != nil {
		return fail(err)
	}
	queenKey := firstNonEmpty(*apiKey, os.Getenv("HIVE_LME_API_KEY"), os.Getenv("HIVE_LIVE_API_KEY"))
	keyEnv := ""
	if queenKey != "" {
		// resolveChatProvider reads this variable (written into each home's
		// configuration below); prefer the environment over argv.
		_ = os.Setenv("HIVE_LME_API_KEY", queenKey)
		keyEnv = "HIVE_LME_API_KEY"
	}
	asker := cli.NewLMERunner(cli.LMERunnerOptions{
		Queen:        *queen,
		Model:        *model,
		APIBase:      *apiBase,
		APIKeyEnvVar: keyEnv,
		Out:          os.Stderr,
	})

	dir := *runDir
	if strings.TrimSpace(dir) == "" {
		dir = filepath.Join(longmemeval.DefaultDataDir(), "runs", time.Now().Format("20060102-150405"))
	}
	logf := func(string, ...any) {}
	if common.verbose {
		logf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}
	summary, report, err := longmemeval.Run(ctx, longmemeval.RunOptions{
		SplitDir:  splitDir,
		RunDir:    dir,
		Variant:   common.dataset,
		Questions: *questions,
		Resume:    *resume,
		Queen:     *queen,
		Model:     *model,
		Timeout:   time.Duration(*timeout * float64(time.Second)),
		KeepHomes: *keepHomes,
		Asker:     asker,
		Judge:     judge,
		Logf:      logf,
	})
	if err != nil {
		return fail(err)
	}
	if report != "" {
		fmt.Println(report)
	}
	if summary != nil {
		fmt.Fprintf(os.Stderr, "results: %s\n", dir)
	}
	return 0
}

func cmdRejudge(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	runRoot := args[0]
	fs := flag.NewFlagSet("rejudge", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	common := &commonFlags{}
	common.bindDataset(fs)
	common.bindJudge(fs)
	concurrency := fs.Int("concurrency", 8, "parallel judge calls")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	splitDir, err := common.splitDir(ctx)
	if err != nil {
		return fail(err)
	}
	judge, err := common.judge()
	if err != nil {
		return fail(err)
	}
	report, _, err := longmemeval.Rejudge(ctx, longmemeval.RejudgeOptions{
		RunDir:      runRoot,
		SplitDir:    splitDir,
		Judge:       judge,
		Concurrency: *concurrency,
		Title:       longmemeval.Label(common.dataset) + " through Hive (re-graded)",
		Logf:        stderrLogf(common.verbose),
	})
	if err != nil {
		return fail(err)
	}
	if report != "" {
		fmt.Println(report)
	}
	return 0
}

func cmdReference(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	condition := args[0]
	if condition != "oracle" && condition != "full-history" {
		fmt.Fprintf(os.Stderr, "reference condition must be oracle or full-history, got %q\n", condition)
		return 2
	}
	fs := flag.NewFlagSet("reference", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	common := &commonFlags{}
	common.bindDataset(fs)
	common.bindJudge(fs)
	out := fs.String("out", "", "results directory (required)")
	questions := fs.String("questions", "all", "all, comma-separated ids, or a count")
	readerModel := fs.String("reader-model", envOr("HIVE_LME_READER_MODEL", longmemeval.OfficialJudgeModel), "reader model")
	readerBase := fs.String("reader-api-base", firstNonEmpty(os.Getenv("HIVE_LME_READER_API_BASE"), os.Getenv("HIVE_LME_JUDGE_API_BASE"), "https://api.openai.com/v1"), "reader endpoint")
	readerKey := fs.String("reader-api-key", "", "reader key (prefer HIVE_LME_READER_API_KEY)")
	concurrency := fs.Int("concurrency", 8, "parallel reader calls")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if strings.TrimSpace(*out) == "" {
		fmt.Fprintln(os.Stderr, "reference: --out is required")
		return 2
	}
	splitDir, err := common.splitDir(ctx)
	if err != nil {
		return fail(err)
	}
	index, err := longmemeval.LoadIndex(splitDir)
	if err != nil {
		return fail(err)
	}
	ids, err := longmemeval.Select(index, *questions)
	if err != nil {
		return fail(err)
	}
	judge, err := common.judge()
	if err != nil {
		return fail(err)
	}
	key := firstNonEmpty(*readerKey, os.Getenv("HIVE_LME_READER_API_KEY"), os.Getenv("HIVE_LME_JUDGE_API_KEY"), os.Getenv("OPENAI_API_KEY"))
	reader := longmemeval.NewAPIClient(*readerBase, key, &http.Client{Timeout: 300 * time.Second})
	report, _, err := longmemeval.Reference(ctx, longmemeval.ReferenceOptions{
		SplitDir:     splitDir,
		OutDir:       *out,
		IDs:          ids,
		Oracle:       condition == "oracle",
		ReaderModel:  *readerModel,
		ReaderClient: reader,
		Judge:        judge,
		Concurrency:  *concurrency,
		Title:        longmemeval.Label(common.dataset) + ", official reader, " + condition,
		Logf:         stderrLogf(common.verbose),
	})
	if err != nil {
		return fail(err)
	}
	if report != "" {
		fmt.Println(report)
	}
	return 0
}

func cmdSummary(_ context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	path := filepath.Join(args[0], "results.jsonl")
	report, summary, err := longmemeval.Summarize(path, "LongMemEval through Hive")
	if err != nil {
		return fail(err)
	}
	if report == "" {
		fmt.Fprintf(os.Stderr, "no results in %s\n", path)
		return 1
	}
	if err := os.WriteFile(filepath.Join(args[0], "summary.txt"), []byte(report+"\n"), 0o644); err != nil {
		return fail(err)
	}
	if summary != nil {
		encoded, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return fail(err)
		}
		if err := os.WriteFile(filepath.Join(args[0], "summary.json"), encoded, 0o644); err != nil {
			return fail(err)
		}
	}
	fmt.Println(report)
	return 0
}

func stderrLogf(verbose bool) longmemeval.Logf {
	if !verbose {
		return nil
	}
	return func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

func fail(err error) int {
	fmt.Fprintf(os.Stderr, "hive-longmemeval: %v\n", err)
	return 1
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envFloatOr(key string, fallback float64) float64 {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
