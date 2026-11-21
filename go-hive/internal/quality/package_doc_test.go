package quality

// Parity-exempt: 包文档基线是 Go 侧仓库护栏——`go list ./...` 里的每个包都应
// 有一行 `// Package <name>` 文档注释；缺注释的包数只减不增，新包必须带
// 文档。上游 Python 常以模块 docstring 表达同类意图，但没有按包计数的等价
// 门禁可对齐。

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// packageDocWaiver mirrors internal/quality/package_doc_waiver.json.
type packageDocWaiver struct {
	Metric                   string `json:"metric"`
	StatedBaselineMissing    int    `json:"stated_baseline_missing"`
	StatedBaselineDocumented int    `json:"stated_baseline_documented"`
	MissingLimit             int    `json:"missing_limit"`
	DocumentedFloor          int    `json:"documented_floor"`
	Note                     string `json:"note"`
}

func loadPackageDocWaiver(t *testing.T, root string) packageDocWaiver {
	t.Helper()
	path := filepath.Join(root, "internal", "quality", "package_doc_waiver.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("package_doc_waiver.json 不可读（internal/quality/package_doc_waiver.json）：%v", err)
	}
	var waiver packageDocWaiver
	if err := json.Unmarshal(data, &waiver); err != nil {
		t.Fatalf("package_doc_waiver.json 解析失败：%v", err)
	}
	if waiver.MissingLimit <= 0 || waiver.DocumentedFloor <= 0 || strings.TrimSpace(waiver.Metric) == "" {
		t.Fatalf("package_doc_waiver.json 缺少 metric/limit：%+v", waiver)
	}
	return waiver
}

// packageDocScan lists the packages in the module (same set as `go list ./...`,
// build constraints respected) and splits them by whether any of their files
// carries a `// Package <name>` doc comment. Paths are module-relative.
func packageDocScan(root string) (documented []string, missing []string, err error) {
	cmd := exec.Command("go", "list", "-f", "{{.Dir}}|{{.Name}}", "./...")
	cmd.Dir = root
	out, listErr := cmd.Output()
	if listErr != nil {
		return nil, nil, fmt.Errorf("go list ./... 失败: %w", listErr)
	}
	fset := token.NewFileSet()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		dir, name := parts[0], parts[1]
		rel, relErr := filepath.Rel(root, dir)
		if relErr != nil {
			return nil, nil, relErr
		}
		rel = filepath.ToSlash(rel)
		entries, readErr := os.ReadDir(dir)
		if readErr != nil {
			return nil, nil, readErr
		}
		hasDoc := false
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			file, parseErr := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.PackageClauseOnly|parser.ParseComments)
			if parseErr != nil || file.Doc == nil || len(file.Doc.List) == 0 {
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(file.Doc.List[0].Text), "// Package") {
				hasDoc = true
				break
			}
		}
		_ = name
		if hasDoc {
			documented = append(documented, rel)
		} else {
			missing = append(missing, rel)
		}
	}
	sort.Strings(documented)
	sort.Strings(missing)
	return documented, missing, nil
}

// packageDocProblems enforces the frozen baseline: missing may not grow and
// documented may not shrink.
func packageDocProblems(documented, missing []string, missingLimit, documentedFloor int) []string {
	var problems []string
	if len(missing) > missingLimit {
		problems = append(problems, fmt.Sprintf("缺少 `// Package` 注释的包 %d 个，超过冻结上限 %d（只减不增）：%s",
			len(missing), missingLimit, strings.Join(missing, ", ")))
	}
	if len(documented) < documentedFloor {
		problems = append(problems, fmt.Sprintf("带 `// Package` 注释的包降到 %d 个，低于冻结下限 %d：%s",
			len(documented), documentedFloor, strings.Join(documented, ", ")))
	}
	return problems
}

// Parity-exempt: 包文档基线门禁本体。
func TestPackageDocBaseline(t *testing.T) {
	root := moduleRoot(t)
	waiver := loadPackageDocWaiver(t, root)
	documented, missing, err := packageDocScan(root)
	if err != nil {
		t.Fatalf("扫描包文档失败: %v", err)
	}
	if len(documented)+len(missing) == 0 {
		t.Fatalf("go list 未返回任何包：门禁失效（root=%s）", root)
	}
	if problems := packageDocProblems(documented, missing, waiver.MissingLimit, waiver.DocumentedFloor); len(problems) > 0 {
		t.Fatalf("包文档基线被破坏（%d 条，metric=%s）:\n  %s", len(problems), waiver.Metric, strings.Join(problems, "\n  "))
	}
	if len(missing) < waiver.MissingLimit || len(documented) > waiver.DocumentedFloor {
		t.Logf("提示：包文档计数已改善（缺 %d / 有 %d），可下调 package_doc_waiver.json 的 missing_limit 或上调 documented_floor",
			len(missing), len(documented))
	}
}

// Parity-exempt: 门禁自检——删掉一个包文档注释必须判定失败。
func TestPackageDocGateSelfTest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module probe\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(dir, pkg), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	docSource := "// Package a documents the a package.\npackage a\n"
	if err := os.WriteFile(filepath.Join(dir, "a", "a.go"), []byte(docSource), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b", "b.go"), []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	documented, missing, err := packageDocScan(dir)
	if err != nil {
		t.Fatalf("扫描探针模块失败: %v", err)
	}
	if len(documented) != 1 || documented[0] != "a" || len(missing) != 1 || missing[0] != "b" {
		t.Fatalf("探针模块分类错误 documented=%v missing=%v", documented, missing)
	}
	if problems := packageDocProblems(documented, missing, 1, 1); len(problems) != 0 {
		t.Fatalf("基线内不应报错: %v", problems)
	}
	// Deleting the doc comment must break the documented floor.
	if err := os.WriteFile(filepath.Join(dir, "a", "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	documented, missing, err = packageDocScan(dir)
	if err != nil {
		t.Fatalf("扫描探针模块失败: %v", err)
	}
	if problems := packageDocProblems(documented, missing, 1, 1); len(problems) == 0 {
		t.Fatalf("删除文档注释后门禁仍然通过：失效（documented=%v missing=%v）", documented, missing)
	}
}
