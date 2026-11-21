package quality

// Parity-exempt: lane 前缀预算是 Go 侧重构护栏——wave 移植期为了绕开并行
// 冲突给函数起了 batchN/batchNLaneM 前缀，造成大量同义 helper 重复。该计数
// 只减不增：拆分/制度化子包时新代码不得再引入带 lane 前缀的标识符。上游
// Python 没有等价债务，也没有可对齐的测试目标。

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// lanePrefixPattern matches the wave-lane identifiers (batch2Get,
// batch7Lane1AsString, ...). The match must start on an identifier boundary;
// the regex consumes the rest of the identifier, so a match ending before a
// word character is impossible by construction.
var lanePrefixPattern = regexp.MustCompile(`batch[0-9]+(?:Lane[0-9]+)?[A-Za-z0-9_]*`)

// lanePrefixWaiver mirrors internal/quality/lane_prefix_waiver.json. The limit
// is frozen: it may be lowered as the debt shrinks, never raised.
type lanePrefixWaiver struct {
	Metric           string `json:"metric"`
	StatedBaseline   int    `json:"stated_baseline"`
	MeasuredAtFreeze int    `json:"measured_at_freeze"`
	Limit            int    `json:"limit"`
	Note             string `json:"note"`
}

// loadLanePrefixWaiver reads the frozen budget next to the other waivers.
func loadLanePrefixWaiver(t *testing.T, root string) lanePrefixWaiver {
	t.Helper()
	path := filepath.Join(root, "internal", "quality", "lane_prefix_waiver.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("lane_prefix_waiver.json 不可读（%s）：%v", "internal/quality/lane_prefix_waiver.json", err)
	}
	var waiver lanePrefixWaiver
	if err := json.Unmarshal(data, &waiver); err != nil {
		t.Fatalf("lane_prefix_waiver.json 解析失败：%v", err)
	}
	if waiver.Limit <= 0 || strings.TrimSpace(waiver.Metric) == "" {
		t.Fatalf("lane_prefix_waiver.json 缺少 metric/limit：%+v", waiver)
	}
	return waiver
}

// scanLanePrefixes collects lane-prefixed identifiers from every non-test .go
// file under internal/ and maps each name to its repo-relative files.
func scanLanePrefixes(root string) (map[string][]string, error) {
	found := map[string]map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case "testdata", "node_modules", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		for _, loc := range lanePrefixPattern.FindAllIndex(data, -1) {
			start, end := loc[0], loc[1]
			if start > 0 && isIdentByte(data[start-1]) {
				continue
			}
			name := string(data[start:end])
			if found[name] == nil {
				found[name] = map[string]bool{}
			}
			found[name][rel] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(found))
	for name, files := range found {
		list := make([]string, 0, len(files))
		for file := range files {
			list = append(list, file)
		}
		sort.Strings(list)
		out[name] = list
	}
	return out, nil
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// lanePrefixProblems enforces the frozen budget and returns one readable
// problem when the debt grew past it.
func lanePrefixProblems(names map[string][]string, limit int) []string {
	if len(names) <= limit {
		return nil
	}
	all := make([]string, 0, len(names))
	for name := range names {
		all = append(all, name)
	}
	sort.Strings(all)
	sample := all
	if len(sample) > 12 {
		sample = sample[:12]
	}
	return []string{fmt.Sprintf(
		"lane 前缀标识符 %d 个，超过冻结上限 %d（只减不增）；示例：%s",
		len(names), limit, strings.Join(sample, ", "),
	)}
}

// Parity-exempt: lane 前缀预算门禁本体；上游无对应债务可对齐。
func TestLanePrefixBudget(t *testing.T) {
	root := moduleRoot(t)
	waiver := loadLanePrefixWaiver(t, root)
	names, err := scanLanePrefixes(root)
	if err != nil {
		t.Fatalf("扫描 internal/ 失败: %v", err)
	}
	if len(names) == 0 {
		t.Fatalf("internal/ 下没有扫描到任何 batch 前缀标识符：门禁失效（root=%s）", root)
	}
	if problems := lanePrefixProblems(names, waiver.Limit); len(problems) > 0 {
		t.Fatalf("lane 前缀预算超限（%d 条，metric=%s）:\n  %s", len(problems), waiver.Metric, strings.Join(problems, "\n  "))
	}
	t.Logf("lane 前缀标识符：%d（冻结上限 %d，brief 标称基线 %d，冻结时实测 %d）", len(names), waiver.Limit, waiver.StatedBaseline, waiver.MeasuredAtFreeze)
	if len(names) < waiver.Limit {
		t.Logf("提示：lane 前缀标识符已降到 %d（≤ 冻结上限 %d），可下调 lane_prefix_waiver.json 的 limit", len(names), waiver.Limit)
	}
}

// Parity-exempt: 门禁自检——注入 batch9Lane9Probe 必须让预算判定失败。
func TestLanePrefixGateSelfTest(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "internal", "probe")
	if err := os.MkdirAll(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	source := "package probe\n\nfunc batch9Lane9Probe() {}\n"
	if err := os.WriteFile(filepath.Join(probe, "probe.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	names, err := scanLanePrefixes(dir)
	if err != nil {
		t.Fatalf("扫描探针根目录失败: %v", err)
	}
	if _, ok := names["batch9Lane9Probe"]; !ok {
		t.Fatalf("探针标识符未被扫描到：%v", names)
	}
	if len(lanePrefixProblems(names, 0)) == 0 {
		t.Fatal("limit=0 时注入了 batch9Lane9Probe 却判定通过：门禁失效")
	}
	if problems := lanePrefixProblems(names, 1); len(problems) != 0 {
		t.Fatalf("limit=1 时探针不应超限: %v", problems)
	}
	// A "_test.go" carrier and a non-boundary prefix must not be counted.
	fixture := filepath.Join(dir, "internal", "fixture.go")
	extra := "package fixture\n\nfunc xbatch9Extra() {}\n"
	if err := os.WriteFile(fixture, []byte(extra), 0o644); err != nil {
		t.Fatal(err)
	}
	testOnly := filepath.Join(dir, "internal", "probe", "probe_test.go")
	if err := os.WriteFile(testOnly, []byte("package probe\n\nfunc batch9Lane9Hidden() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	names, err = scanLanePrefixes(dir)
	if err != nil {
		t.Fatalf("扫描探针根目录失败: %v", err)
	}
	if _, ok := names["xbatch9Extra"]; ok {
		t.Fatal("标识符中间的 batch 前缀被误计")
	}
	if _, ok := names["batch9Lane9Hidden"]; ok {
		t.Fatal("_test.go 中的标识符被误计")
	}
}
