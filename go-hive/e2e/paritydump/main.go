//go:build ignore

// paritydump: dump RecallPastConversations results for a fixed query set so
// two builds of the same store (e.g. before/after a performance change) can be
// compared byte-for-byte.
//
// Deliberately outside the product build (same `ignore` tag convention as
// e2e/perfrecall15k). Build it by naming the file, e.g.:
//
//	CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
//	  go build -o /tmp/paritydump e2e/paritydump/main.go
//
// Usage:
//
//	paritydump -home HOME -queries queries.txt -out results.jsonl
//	          [-owners queen_alpha,queen_beta,queen_gamma] [-exclude=true]
//
// Output is JSON Lines, one record per (owner, query, exclude variant), in a
// fixed order: owners as given, queries in file order, the unfiltered call
// before the excluded-session call. Each record carries every returned turn
// with its score, so `cmp` on two dumps proves identical matching *and*
// identical scores. Companion runner: go_hive/scripts/perf_recall_parity.sh.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/memory"
)

type dumpTurn struct {
	Session string  `json:"session"`
	Ordinal int     `json:"ordinal"`
	User    string  `json:"user"`
	Reply   string  `json:"reply"`
	Score   float64 `json:"score"`
}

type dumpRecord struct {
	Kind    string     `json:"kind"`
	Owner   string     `json:"owner"`
	Query   string     `json:"query"`
	Exclude string     `json:"exclude"`
	Turns   []dumpTurn `json:"turns"`
}

type dumpHeader struct {
	Kind    string   `json:"kind"`
	Go      string   `json:"go"`
	Owners  []string `json:"owners"`
	Queries int      `json:"queries"`
}

func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			out = append(out, line)
		}
	}
	return out, scanner.Err()
}

func main() {
	home := flag.String("home", "", "HIVE_HOME with the generated store and recall index")
	queriesPath := flag.String("queries", "", "file with one query per line")
	outPath := flag.String("out", "", "output JSONL path")
	ownersFlag := flag.String("owners", "queen_alpha,queen_beta,queen_gamma", "comma-separated queen owners")
	excludeVariants := flag.Bool("exclude", true, "also dump each query with the scope's first session excluded")
	flag.Parse()
	if *home == "" || *queriesPath == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "paritydump: -home, -queries and -out are required")
		os.Exit(2)
	}

	queries, err := readLines(*queriesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "paritydump: read queries: %v\n", err)
		os.Exit(1)
	}
	var owners []string
	for _, owner := range strings.Split(*ownersFlag, ",") {
		if owner = strings.TrimSpace(owner); owner != "" {
			owners = append(owners, owner)
		}
	}

	out, err := os.Create(*outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "paritydump: create out: %v\n", err)
		os.Exit(1)
	}
	defer out.Close()
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)

	if err := encoder.Encode(dumpHeader{Kind: "header", Go: runtime.Version(), Owners: owners, Queries: len(queries)}); err != nil {
		fmt.Fprintf(os.Stderr, "paritydump: write header: %v\n", err)
		os.Exit(1)
	}

	records := 0
	turns := 0
	for _, owner := range owners {
		ref := memory.ScopeRef{Scope: memory.SearchScopeQueens, Owner: owner}
		sessions := memory.SearchListSessions(*home, ref.Scope, ref.Owner, "", nil, nil)
		sort.Strings(sessions)
		exclude := ""
		if len(sessions) > 0 {
			exclude = sessions[0]
		}
		// One unrecorded warm-up call per owner so any index refresh the run
		// needs has already landed (and, for cached builds, warmed) before the
		// first recorded query — both binaries then record the same state.
		memory.RecallPastConversations(*home, ref, queries[0], memory.PastRecallOptions{ExcludeSession: exclude})

		for _, query := range queries {
			variants := []string{""}
			if *excludeVariants && exclude != "" {
				variants = append(variants, exclude)
			}
			for _, variant := range variants {
				got := memory.RecallPastConversations(*home, ref, query, memory.PastRecallOptions{ExcludeSession: variant})
				record := dumpRecord{Kind: "query", Owner: owner, Query: query, Exclude: variant, Turns: make([]dumpTurn, 0, len(got))}
				for _, turn := range got {
					record.Turns = append(record.Turns, dumpTurn{
						Session: turn.Session, Ordinal: turn.Ordinal,
						User: turn.User, Reply: turn.Reply, Score: turn.Score,
					})
				}
				if err := encoder.Encode(record); err != nil {
					fmt.Fprintf(os.Stderr, "paritydump: write record: %v\n", err)
					os.Exit(1)
				}
				records++
				turns += len(got)
			}
		}
		fmt.Printf("paritydump: %-12s queries=%d exclude_variant=%v\n", owner, len(queries), *excludeVariants && exclude != "")
	}
	if err := out.Sync(); err != nil {
		fmt.Fprintf(os.Stderr, "paritydump: sync: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("paritydump: wrote %s records=%d turns=%d\n", *outPath, records, turns)
}
