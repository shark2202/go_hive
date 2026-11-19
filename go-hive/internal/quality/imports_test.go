package quality

// Parity-exempt: Go 特有的静态分层门禁（go/parser 扫 import 图）；上游 Python
// 没有等价测试，也没有可对齐的测试目标。

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// modulePath is the module's import prefix. Internal packages live under it,
// so a dependency on internal/cli is exactly modulePath + "/internal/cli" or a
// subpackage of it.
const modulePath = "github.com/aden-hive/hive-go"

// layerRule forbids one internal package (subpackages included) from importing
// another. Keeping the boundaries as data makes a new rule a one-line change
// and lets TestLayerGateDetectsViolation exercise the matcher directly.
type layerRule struct {
	pkg       string // importer, relative to internal/
	forbidden string // imported package, relative to internal/
	why       string
}

var layerRules = []layerRule{
	{
		pkg:       "server",
		forbidden: "cli",
		why:       "cli 是 server 的调用方（loader 构造 ChatRuntime）；反向依赖会把装配层与命令行锁成环",
	},
	{
		pkg:       "tools",
		forbidden: "server",
		why:       "tools 是执行层，server 依赖它做适配；反向依赖会让工具离开 HTTP/装配上下文就无法复用",
	},
	{
		pkg:       "tools",
		forbidden: "cli",
		why:       "cli 只是工具的宿主入口，工具层不得知道命令行的存在",
	},
	{
		pkg:       "agentloop",
		forbidden: "server",
		why:       "agentloop 是内嵌的循环核心，必须能被 server 之外的宿主（colony/queen）直接驱动",
	},
	{
		pkg:       "memory",
		forbidden: "server",
		why:       "memory 是底层存储，server 依赖它做 recall；反向依赖会把存储绑死在 HTTP 生命周期上",
	},
}

// importsInternal reports whether an import path resolves to internal/<name> or
// one of its subpackages. The trailing separator is what keeps
// internal/clihelper from being mistaken for internal/cli.
func importsInternal(path, name string) bool {
	prefix := modulePath + "/internal/" + name
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// ruleAppliesTo reports whether a file path (module-relative, slash-separated)
// belongs to the rule's importing package or a subpackage of it.
func ruleAppliesTo(relPath, pkg string) bool {
	prefix := "internal/" + pkg
	return relPath == prefix || strings.HasPrefix(relPath, prefix+"/")
}

// evaluateImports applies every rule to one file's import list and returns one
// readable message per violation, prefixed with the file path.
func evaluateImports(rules []layerRule, relPath string, imports []string) []string {
	var out []string
	for _, rule := range rules {
		if !ruleAppliesTo(relPath, rule.pkg) {
			continue
		}
		for _, imp := range imports {
			if !importsInternal(imp, rule.forbidden) {
				continue
			}
			out = append(out, fmt.Sprintf(
				"%s: import %q —— 违反 internal/%s 不得依赖 internal/%s（%s）",
				relPath, imp, rule.pkg, rule.forbidden, rule.why,
			))
		}
	}
	return out
}

// Parity-exempt: Go 特有的静态分层门禁（go/parser 扫 import 图），上游 Python 没有等价测试。
//
// TestInternalLayering walks every .go file under internal/ (tests included)
// and fails with the package and the offending import for each inversion.
func TestInternalLayering(t *testing.T) {
	root := moduleRoot(t)
	internalDir := filepath.Join(root, "internal")

	fset := token.NewFileSet()
	scanned := map[string]int{}
	var violations []string

	walkErr := filepath.WalkDir(internalDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		imports := make([]string, 0, len(file.Imports))
		for _, spec := range file.Imports {
			imports = append(imports, strings.Trim(spec.Path.Value, `"`))
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		for _, rule := range layerRules {
			if ruleAppliesTo(rel, rule.pkg) {
				scanned[rule.pkg]++
				break
			}
		}
		violations = append(violations, evaluateImports(layerRules, rel, imports)...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("扫描 %s 失败: %v", internalDir, walkErr)
	}

	// A gate that silently scans nothing always passes. Every rule's importer
	// must contribute at least one file, or the rule (or the walk) is broken.
	for _, rule := range layerRules {
		if scanned[rule.pkg] == 0 {
			t.Fatalf("internal/%s 下没有扫描到任何 Go 文件（root=%s）：分层门禁失效", rule.pkg, root)
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("分层依赖被破坏（%d 条）:\n  %s", len(violations), strings.Join(violations, "\n  "))
	}
}

// Parity-exempt: 分层门禁的自检（证明检查器能报错），属于 Go 静态门禁的元测试，上游无对应目标。
//
// TestLayerGateDetectsViolation proves the checker can fail: a synthetic
// inverted import must be reported, allowed directions must not be, and a
// package whose name merely shares the prefix must not be caught.
func TestLayerGateDetectsViolation(t *testing.T) {
	bad := evaluateImports(layerRules, "internal/server/fake.go", []string{
		modulePath + "/internal/cli",
		"context",
	})
	if len(bad) != 1 || !strings.Contains(bad[0], "/internal/cli") {
		t.Fatalf("合成的 internal/server -> internal/cli 依赖未被报出: %v", bad)
	}

	allowed := evaluateImports(layerRules, "internal/server/ok.go", []string{
		modulePath + "/internal/tools",
		modulePath + "/internal/agentloop",
	})
	if len(allowed) != 0 {
		t.Fatalf("允许的方向被误报: %v", allowed)
	}

	prefixTrap := evaluateImports(layerRules, "internal/server/ok.go", []string{
		modulePath + "/internal/clihelper",
	})
	if len(prefixTrap) != 0 {
		t.Fatalf("前缀相同的其它包被误报: %v", prefixTrap)
	}
}

// moduleRoot walks up from this test file to the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败：无法定位模块根目录")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("从 %s 向上未找到 go.mod", file)
		}
		dir = parent
	}
}
