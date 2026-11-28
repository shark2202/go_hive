package astanalyze

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func truePtr() *bool { v := true; return &v }

// Parity-exempt: Go-only post-1.0 ast_analyze core; no Python counterpart.
func TestAnalyzeGoUnitsGraphRisks(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/app\n\ngo 1.26\n")
	write(t, root, "main.go", `package main

import (
	"fmt"
	"time"

	"example.com/app/lib"
)

func main() {
	go func() { fmt.Println(time.Now()) }()
	lib.Run()
}
`)
	write(t, root, "lib/lib.go", `package lib

import "fmt"

func Run() { var v any = 1; fmt.Println(v) }

var A1 = 1
var A2 = 2
var A3 = 3
var A4 = 4
var A5 = 5
var A6 = 6
var A7 = 7
var A8 = 8
var A9 = 9
var A10 = 10
var A11 = 11
var A12 = 12
var A13 = 13
var A14 = 14
var A15 = 15
var A16 = 16
var A17 = 17
var A18 = 18
var A19 = 19
var A20 = 20
`)
	write(t, root, "lib/lib_test.go", `package lib

import "testing"

// Parity-exempt: Go-only post-1.0 ast_analyze core; no Python counterpart.
func TestRun(t *testing.T) { Run() }
`)

	cfg := DefaultConfig()
	report, err := Analyze(context.Background(), cfg, root, Request{Path: root, IncludeTests: truePtr()})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if report.Language != "go" {
		t.Fatalf("language = %q", report.Language)
	}
	if report.Summary.Units != 2 {
		t.Fatalf("units = %d: %+v", report.Summary.Units, report.Units)
	}
	byID := map[string]UnitReport{}
	for _, unit := range report.Units {
		byID[unit.ID] = unit
	}
	lib := byID["lib/"]
	if lib.ID == "" {
		t.Fatalf("lib unit missing: %+v", report.Units)
	}
	main := byID["main.go"]
	if main.ID == "" {
		t.Fatalf("main unit missing: %+v", report.Units)
	}
	if lib.Wave != 0 || main.Wave != 1 {
		t.Fatalf("waves lib=%d main=%d", lib.Wave, main.Wave)
	}
	if len(main.InternalDeps) != 1 || main.InternalDeps[0] != "lib/" {
		t.Fatalf("main deps = %+v", main.InternalDeps)
	}
	if lib.FanIn != 1 {
		t.Fatalf("lib fan_in = %d", lib.FanIn)
	}
	if lib.DirectTests != 1 {
		t.Fatalf("lib direct_tests = %d", lib.DirectTests)
	}
	if len(main.Risks) == 0 {
		t.Fatalf("main risks empty")
	}
	foundConcurrency := false
	for _, risk := range main.Risks {
		if risk.Risk == RiskConcurrency {
			foundConcurrency = true
		}
	}
	if !foundConcurrency {
		t.Fatalf("main risks = %+v, want concurrency", main.Risks)
	}
	if len(report.StartHere) == 0 {
		t.Fatalf("start_here empty: %+v", report.Summary)
	}
}

// Parity-exempt: Go-only post-1.0 ast_analyze core; no Python counterpart.
func TestAnalyzePythonAndTS(t *testing.T) {
	root := t.TempDir()
	write(t, root, "py/lib/util.py", "def run():\n    return eval('1')\n")
	write(t, root, "py/app/main.py", "from py.lib.util import run\n\ndef main():\n    return run()\n")
	write(t, root, "ts/lib/util.ts", "export function run(x: any) { return eval('1') }\n")
	write(t, root, "ts/app.ts", "import { run } from './lib/util'\nexport const x = run(1)\n")
	write(t, root, "ts/ui/view.tsx", "export const View = () => null\n")

	cfg := DefaultConfig()
	report, err := Analyze(context.Background(), cfg, root, Request{Path: root})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	ids := map[string]bool{}
	for _, unit := range report.Units {
		ids[unit.ID] = true
	}
	for _, want := range []string{"py/lib/", "py/app/", "ts/lib/", "ts/", "ts/ui/"} {
		if !ids[want] {
			t.Fatalf("unit %q missing: %+v", want, report.Units)
		}
	}
	if report.Summary.UIFiles != 1 {
		t.Fatalf("ui files = %d", report.Summary.UIFiles)
	}
	byID := map[string]UnitReport{}
	for _, unit := range report.Units {
		byID[unit.ID] = unit
	}
	if byID["py/app/"].Wave != 1 || byID["py/lib/"].Wave != 0 {
		t.Fatalf("python waves app=%d lib=%d", byID["py/app/"].Wave, byID["py/lib/"].Wave)
	}
	if byID["ts/"].Wave != 1 || byID["ts/lib/"].Wave != 0 {
		t.Fatalf("ts waves app=%d lib=%d", byID["ts/"].Wave, byID["ts/lib/"].Wave)
	}
	if byID["ts/ui/"].Status != StatusKeep {
		t.Fatalf("ui status = %q", byID["ts/ui/"].Status)
	}
	if len(byID["py/lib/"].Risks) == 0 || len(byID["ts/lib/"].Risks) == 0 {
		t.Fatalf("risk detection missing: py=%+v ts=%+v", byID["py/lib/"].Risks, byID["ts/lib/"].Risks)
	}
}

// Parity-exempt: Go-only post-1.0 ast_analyze core; no Python counterpart.
func TestAnalyzePathsAndConfig(t *testing.T) {
	root := t.TempDir()
	write(t, root, "main.go", "package main\n\nfunc main() {}\n")
	write(t, root, "other.go", "package main\n\nfunc other() {}\n")

	if _, err := Analyze(context.Background(), DefaultConfig(), root, Request{Path: "main.go"}); !IsKind(err, ErrPathNotAbsolute) {
		t.Fatalf("relative path err = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.go")
	if _, err := Analyze(context.Background(), DefaultConfig(), root, Request{Path: outside}); !IsKind(err, ErrPathEscape) {
		t.Fatalf("outside path err = %v", err)
	}
	disabled := DefaultConfig()
	disabled.Enabled = false
	if _, err := Analyze(context.Background(), disabled, root, Request{Path: root}); !IsKind(err, ErrConfigDisabled) {
		t.Fatalf("disabled err = %v", err)
	}
	cfg := DefaultConfig()
	cfg.MaxFiles = 1
	report, err := Analyze(context.Background(), cfg, root, Request{Path: root})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !strings.Contains(strings.Join(report.Warnings, "\n"), "max_files") {
		t.Fatalf("warnings = %v", report.Warnings)
	}
}

// Parity-exempt: Go-only post-1.0 ast_analyze core; no Python counterpart.
func TestConfigLoadHomeOnly(t *testing.T) {
	home := t.TempDir()
	cfg := LoadConfig(home)
	if !cfg.Enabled || cfg.LoadError != nil {
		t.Fatalf("missing config = %+v", cfg)
	}
	if err := os.WriteFile(filepath.Join(home, "ast_analyze.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = LoadConfig(home)
	if cfg.Enabled || cfg.LoadError == nil {
		t.Fatalf("malformed config = %+v", cfg)
	}
	t.Setenv("HIVE_AST_ANALYZE", "0")
	if cfg = LoadConfig(t.TempDir()); cfg.Enabled {
		t.Fatal("HIVE_AST_ANALYZE=0 did not disable")
	}
}

// Parity-exempt: Go-only post-1.0 ast_analyze core; no Python counterpart.
func TestAnalyzeMaxUnits(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a/a.go", "package a\n\nfunc A() {}\n")
	write(t, root, "b/b.go", "package b\n\nfunc B() {}\n")
	cfg := DefaultConfig()
	cfg.MaxUnits = 1
	report, err := Analyze(context.Background(), cfg, root, Request{Path: root})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if report.Summary.Units != 1 {
		t.Fatalf("units = %d", report.Summary.Units)
	}
	if !strings.Contains(strings.Join(report.Warnings, "\n"), "max_units") {
		t.Fatalf("warnings = %v", report.Warnings)
	}
}

// Parity-exempt: Go-only post-1.0 ast_analyze core; no Python counterpart.
func TestAnalyzeWorkerDeterminism(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", "module example.com/app\n\ngo 1.26\n")
	write(t, root, "main.go", "package main\n\nimport \"example.com/app/lib\"\n\nfunc main() { lib.Run() }\n")
	write(t, root, "lib/lib.go", "package lib\n\nfunc Run() {}\n")
	write(t, root, "lib/lib_test.go", "package lib\n\nimport \"testing\"\n\nfunc TestRun(t *testing.T) { Run() }\n")

	single := DefaultConfig()
	single.Workers = 1
	first, err := Analyze(context.Background(), single, root, Request{Path: root, IncludeTests: truePtr()})
	if err != nil {
		t.Fatal(err)
	}
	parallel := DefaultConfig()
	parallel.Workers = 4
	second, err := Analyze(context.Background(), parallel, root, Request{Path: root, IncludeTests: truePtr()})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatalf("worker output differs:\n%s\n%s", a, b)
	}
}
