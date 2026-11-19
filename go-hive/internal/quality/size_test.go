package quality

// Parity-exempt: 规模门禁是 Go 侧重构护栏（按文件/函数行数冻结），上游 Python
// 没有等价测试可对齐。

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 预算：新增代码不得超过这两条线；已有超限文件走 allowlist，且登记的
// 上限是「冻结值」——文件可以变短，不能再长。
const (
	maxFileLines = 1500
	maxFuncLines = 200
)

// sizeWaiver freezes one already-oversized file at its current size.
type sizeWaiver struct {
	maxLines int
	reason   string
}

// fileWaivers: module-relative path -> frozen ceiling + why it is tolerated.
var fileWaivers = map[string]sizeWaiver{
	"internal/tools/integrations/batch7_lane1.go": {2276, "batch7 工具移植按 lane 整批落地：一个文件是一批 action 的定义与参数 schema，拆分列入后续重构；上限冻结，只减不增"},
	"internal/tools/integrations/batch7_lane2.go": {1973, "batch7 工具移植按 lane 整批落地：一个文件是一批 action 的定义与参数 schema，拆分列入后续重构；上限冻结，只减不增"},
	"internal/tools/integrations/batch7_lane3.go": {2086, "batch7 工具移植按 lane 整批落地：一个文件是一批 action 的定义与参数 schema，拆分列入后续重构；上限冻结，只减不增"},
	"internal/tools/integrations/batch7_lane4.go": {3031, "batch7 工具移植按 lane 整批落地：一个文件是一批 action 的定义与参数 schema，拆分列入后续重构；上限冻结，只减不增"},
	"internal/tools/integrations/batch7_lane5.go": {2848, "batch7 工具移植按 lane 整批落地：一个文件是一批 action 的定义与参数 schema，拆分列入后续重构；上限冻结，只减不增"},
	"internal/tools/integrations/batch7_lane6.go": {1838, "batch7 工具移植按 lane 整批落地：一个文件是一批 action 的定义与参数 schema，拆分列入后续重构；上限冻结，只减不增"},
	"internal/tools/integrations/batch7_lane7.go": {2532, "batch7 工具移植按 lane 整批落地：一个文件是一批 action 的定义与参数 schema，拆分列入后续重构；上限冻结，只减不增"},
	"internal/tools/integrations/batch7_lane8.go": {2255, "batch7 工具移植按 lane 整批落地：一个文件是一批 action 的定义与参数 schema，拆分列入后续重构；上限冻结，只减不增"},
	"internal/tools/integrations/batch3_pdf.go":   {2014, "batch3 PDF 工具整批移植（action 定义 + 参数 schema + 错误映射）；拆分列入后续重构，上限冻结"},
	"internal/tools/integrations/batch3_excel.go": {1818, "batch3 Excel 工具整批移植（action 定义 + 参数 schema + 错误映射）；拆分列入后续重构，上限冻结"},
	"internal/tools/integrations/catalog_gen.go":  {2074, "生成文件（gen_catalog.py 产出，DO NOT EDIT）：工具目录表的行数由上游 @mcp.tool 数量决定，不应手改"},
	"internal/credentials/specs_gen.go":           {2523, "生成文件（gen_credential_specs.py 产出，DO NOT EDIT）：85 个模块的凭据规格表，行数由上游注册表决定"},
	"internal/orchestrator/safe_eval.go":          {2734, "安全求值器的运算符与内建函数分派表集中在一个文件：表驱动结构改动一处就要整批对照，拆分的收益低于保持单表可审"},
	"internal/mcpregistry/cli.go":                 {1746, "MCP 注册表子命令（import/export/list/…）共享一套 flag 解析与错误输出；先冻结规模，拆分留待命令族稳定"},
	"internal/cli/skill.go":                       {1601, "skill 子命令族与安装/测试流程集中在一个文件；拆分列入后续重构，上限冻结"},
	"internal/agentloop/conversation.go":          {1549, "会话转换与消息编排集中在一个文件，循环核心语义都在这；拆分需先稳定接口，暂冻结规模"},
}

// funcWaivers: "<module-relative file>::<func label>" -> frozen ceiling + why.
var funcWaivers = map[string]sizeWaiver{
	"internal/cli/skill.go::(*Runner).cmdSkillTest":                 {311, "skill test 子命令：定位 skill、跑用例、汇总输出串在一条流程里；拆分需先抽出报告器"},
	"internal/agentloop/loop.go::(*AgentLoop).ExecuteTurn":          {281, "一轮对话的主流程（模型调用、工具分派、事件发布）共享同一条错误路径；拆分需先稳定阶段接口"},
	"internal/maintenance/janitor.go::RunOnce":                      {239, "janitor 单轮清理按类别顺序执行，共享同一份报告与安全上下文；按类别拆分改的是行为顺序"},
	"internal/server/routes_session_create.go::handleCreateSession": {227, "会话创建路由：参数校验、home 解析、事件总线接线在同一事务边界内"},
	"internal/tools/integrations/batch3_scrape.go::webScrapeAction": {217, "抓取工具的 fetch/解析/截断分支集中在一处；拆分需先抽出抓取器"},
}

// Parity-exempt: Go 侧文件规模护栏（按行数冻结 + allowlist），上游 Python 没有对应测试。
//
// TestFileSizeBudget fails for any non-test .go file under internal/ that is
// over maxFileLines without a waiver, and for any waived file that grew past
// its frozen ceiling.
func TestFileSizeBudget(t *testing.T) {
	root := moduleRoot(t)
	firstSeen := map[string]int{}

	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
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
		firstSeen[rel] = countLines(data)
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 internal/ 失败: %v", err)
	}
	if len(firstSeen) == 0 {
		t.Fatalf("internal/ 下没有扫描到非测试 Go 文件（root=%s）：规模门禁失效", root)
	}

	var problems []string
	for rel, lines := range firstSeen {
		if waiver, ok := fileWaivers[rel]; ok {
			if lines > waiver.maxLines {
				problems = append(problems, fmt.Sprintf(
					"%s: %d 行，超过冻结上限 %d（allowlist 理由：%s）",
					rel, lines, waiver.maxLines, waiver.reason,
				))
			}
			continue
		}
		if lines > maxFileLines {
			problems = append(problems, fmt.Sprintf(
				"%s: %d 行，超过上限 %d；请拆分文件，或在 fileWaivers 中登记冻结上限与理由",
				rel, lines, maxFileLines,
			))
		}
	}
	for rel := range fileWaivers {
		if _, ok := firstSeen[rel]; !ok {
			problems = append(problems, fmt.Sprintf("fileWaivers 条目 %s 找不到对应文件；请删除失效条目", rel))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("文件规模超限（%d 条，上限 %d 行）:\n  %s", len(problems), maxFileLines, strings.Join(problems, "\n  "))
	}

	for rel, waiver := range fileWaivers {
		if lines := firstSeen[rel]; lines <= maxFileLines {
			t.Logf("提示：%s 已降到 %d 行（≤ %d），可删除其 allowlist 条目（原冻结上限 %d）", rel, lines, maxFileLines, waiver.maxLines)
		}
	}
}

// Parity-exempt: Go 侧函数规模护栏（按行数冻结 + allowlist），上游 Python 没有对应测试。
//
// TestFuncSizeBudget applies the same rule to function bodies: over
// maxFuncLines requires a waiver, and a waived function may not grow.
func TestFuncSizeBudget(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	seen := map[string]int{}
	parsed := 0

	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		parsed++
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			lines := fset.Position(fn.End()).Line - fset.Position(fn.Pos()).Line + 1
			seen[rel+"::"+funcLabel(fn)] = lines
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描 internal/ 失败: %v", err)
	}
	if parsed == 0 || len(seen) == 0 {
		t.Fatalf("internal/ 下没有解析到函数（parsed=%d）：规模门禁失效", parsed)
	}

	var problems []string
	for key, lines := range seen {
		if waiver, ok := funcWaivers[key]; ok {
			if lines > waiver.maxLines {
				problems = append(problems, fmt.Sprintf(
					"%s: %d 行，超过冻结上限 %d（allowlist 理由：%s）",
					key, lines, waiver.maxLines, waiver.reason,
				))
			}
			continue
		}
		if lines > maxFuncLines {
			problems = append(problems, fmt.Sprintf(
				"%s: %d 行，超过上限 %d；请拆分函数，或在 funcWaivers 中登记冻结上限与理由",
				key, lines, maxFuncLines,
			))
		}
	}
	for key := range funcWaivers {
		if _, ok := seen[key]; !ok {
			problems = append(problems, fmt.Sprintf("funcWaivers 条目 %s 找不到对应函数；请删除失效条目", key))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("函数规模超限（%d 条，上限 %d 行）:\n  %s", len(problems), maxFuncLines, strings.Join(problems, "\n  "))
	}
}

// Parity-exempt: allowlist 自检（空理由、无意义豁免必须被拒），属于门禁元测试，上游无对应目标。
//
// TestSizeWaiverReasons locks the allowlists' intent: an empty reason (or a
// ceiling at/below the default budget) is a silent waiver and fails.
func TestSizeWaiverReasons(t *testing.T) {
	for rel, waiver := range fileWaivers {
		if strings.TrimSpace(waiver.reason) == "" {
			t.Errorf("fileWaivers[%s] 缺少理由", rel)
		}
		if waiver.maxLines <= maxFileLines {
			t.Errorf("fileWaivers[%s] 冻结上限 %d 不高于默认上限 %d：这条豁免没有意义", rel, waiver.maxLines, maxFileLines)
		}
	}
	for key, waiver := range funcWaivers {
		if strings.TrimSpace(waiver.reason) == "" {
			t.Errorf("funcWaivers[%s] 缺少理由", key)
		}
		if waiver.maxLines <= maxFuncLines {
			t.Errorf("funcWaivers[%s] 冻结上限 %d 不高于默认上限 %d：这条豁免没有意义", key, waiver.maxLines, maxFuncLines)
		}
	}
}

// countLines counts newline-terminated lines, adding one for a final line
// without a trailing newline.
func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := bytes.Count(data, []byte("\n"))
	if data[len(data)-1] != '\n' {
		n++
	}
	return n
}

// funcLabel renders a function as Name or (Recv).Name / (*Recv).Name, matching
// the keys used in funcWaivers.
func funcLabel(fn *ast.FuncDecl) string {
	if fn.Name == nil {
		return "?"
	}
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return "(" + typeLabel(fn.Recv.List[0].Type) + ")." + fn.Name.Name
}

func typeLabel(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeLabel(t.X)
	case *ast.IndexExpr:
		return typeLabel(t.X) + "[" + typeLabel(t.Index) + "]"
	case *ast.IndexListExpr:
		args := make([]string, 0, len(t.Indices))
		for _, idx := range t.Indices {
			args = append(args, typeLabel(idx))
		}
		return typeLabel(t.X) + "[" + strings.Join(args, ", ") + "]"
	}
	return "?"
}

// moduleRoot is defined in imports_test.go; both files share this package.
