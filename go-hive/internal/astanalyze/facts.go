package astanalyze

import (
	"context"
	"regexp"
	"strings"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

var quoted = regexp.MustCompile(`["']([^"']+)["']`)
var goImportPath = regexp.MustCompile(`"([^"]+)"`)

// loopTypes are the per-language loop node kinds.
var loopTypes = map[string]map[string]bool{
	"go":         {"for_statement": true},
	"python":     {"for_statement": true, "while_statement": true},
	"typescript": {"for_statement": true, "for_in_statement": true, "for_of_statement": true, "while_statement": true, "do_statement": true},
	"tsx":        {"for_statement": true, "for_in_statement": true, "for_of_statement": true, "while_statement": true, "do_statement": true},
	"javascript": {"for_statement": true, "for_in_statement": true, "for_of_statement": true, "while_statement": true, "do_statement": true},
}

// importTypes are node kinds that carry an import specifier.
var importTypes = map[string]map[string]bool{
	"go":         {"import_spec": true},
	"python":     {"import_statement": true, "import_from_statement": true},
	"typescript": {"import_statement": true, "export_statement": true, "call_expression": true},
	"tsx":        {"import_statement": true, "export_statement": true, "call_expression": true},
	"javascript": {"import_statement": true, "export_statement": true, "call_expression": true},
}

var riskTextTypes = map[string]bool{
	"call": true, "call_expression": true, "function_call": true, "method_invocation": true,
	"identifier": true, "property_identifier": true, "type_annotation": true, "type_identifier": true,
	"string": true, "string_literal": true, "interpreted_string_literal": true, "raw_string_literal": true,
	"import_statement": true, "import_from_statement": true, "import_spec": true,
	"go_statement": true, "select_statement": true, "channel_type": true,
	"interface_type": true, "type_switch_statement": true,
	"try_statement": true, "raise_statement": true, "throw_statement": true,
	"generator_function": true, "generator_function_declaration": true,
	"await_expression": true, "binary_expression": true,
}

type riskRule struct {
	risk     Risk
	types    map[string]bool
	contains []string
}

var riskRules = []riskRule{
	{risk: RiskDynamicCode, contains: []string{"eval(", "exec(", "new Function(", "plugin.Open(", "compile(", "import("}},
	{risk: RiskReflection, contains: []string{"reflect.", "getattr(", "setattr(", "Proxy(", "Reflect.", "__dict__"}},
	{risk: RiskStringOffsets, contains: []string{".slice(", ".substring(", ".substr(", ".indexOf(", ".charCodeAt(", "[]byte(", "substr(", "strlen(", "strcpy(", "sprintf("}},
	{risk: RiskRegexSyntax, contains: []string{"(?=", "(?<=", "(?<!", `\1`, `\2`}},
	{risk: RiskDynamicRegex, contains: []string{"new RegExp(", "re.compile(", "regexp.MustCompile("}},
	{risk: RiskConcurrency, types: map[string]bool{"go_statement": true, "select_statement": true, "channel_type": true}, contains: []string{"threading", "asyncio", "worker_threads", "multiprocessing"}},
	{risk: RiskUnsafe, contains: []string{"unsafe.", `import "C"`, "ctypes", "native addon"}},
	{risk: RiskPlatformSpecific, contains: []string{"//go:build", "syscall.", "runtime.GOOS", "process.platform", "sys.platform"}},
	{risk: RiskDynamicTyping, contains: []string{"interface{}", " any", ": any", "Any"}},
	{risk: RiskExceptions, types: map[string]bool{"try_statement": true, "raise_statement": true, "throw_statement": true}, contains: []string{"panic(", "recover(", "raise ", "except ", "throw ", "catch "}},
	{risk: RiskAsync, contains: []string{"async ", "await ", "Promise"}},
	{risk: RiskGenerators, types: map[string]bool{"generator_function": true, "generator_function_declaration": true}, contains: []string{"yield ", "function*"}},
	{risk: RiskClockRandom, contains: []string{"time.Now(", "rand.", "datetime.now(", "Math.random(", "Date.now("}},
	{risk: RiskWebAPIs, contains: []string{"net/url", "urllib.parse", "URL(", "TextEncoder", "atob("}},
	{risk: RiskIntl, contains: []string{"Intl.", "localeCompare"}},
	{risk: RiskDateParsing, contains: []string{"Date.parse", "new Date("}},
	{risk: RiskMonkeypatching, contains: []string{"setattr(", "__dict__"}},
	{risk: RiskAlreadyCompiled, contains: []string{"ctypes", "napi", ".so"}},
	{risk: RiskUICode, contains: []string{"react", "react-dom", "vue", "svelte", "solid-js", "preact"}},
}

// scanFacts parses one file and collects its facts.
func scanFacts(ctx context.Context, cfg Config, file sourceFile) FileFacts {
	facts := FileFacts{
		Path:  file.Rel,
		Kind:  file.Kind,
		LOC:   countLOC(file.Data, file.Lang.Name()),
		Risks: map[Risk][]int{},
	}
	parser := gotreesitter.NewParser(file.Lang.Grammar())
	if cfg.ParseTimeout > 0 {
		// #nosec G115 -- ParseTimeout is a positive time.Duration; Microseconds() is checked >0 before conversion.
		parser.SetTimeoutMicros(uint64(cfg.ParseTimeout.Microseconds()))
	}
	tree, err := parser.Parse(file.Data)
	if err != nil || tree == nil {
		facts.ParseErrors = 1
		return facts
	}
	defer tree.Release()
	grammar := file.Lang.Grammar()
	langName := strings.ToLower(file.Lang.Name())
	seenRiskLine := map[Risk]map[int]bool{}
	platforms := map[Platform]bool{}

	var walk func(*gotreesitter.Node)
	walk = func(node *gotreesitter.Node) {
		if node == nil || ctx.Err() != nil {
			return
		}
		typ := node.Type(grammar)
		if node.IsError() || node.IsMissing() {
			facts.ParseErrors++
		}
		line := int(node.StartPoint().Row) + 1
		if loopTypes[langName][typ] {
			facts.Loops++
		}
		if importTypes[langName][typ] {
			text := node.Text(file.Data)
			for _, spec := range importSpecifiers(langName, typ, text) {
				facts.Imports = append(facts.Imports, spec)
				if platform, ok := platformOf(langName, spec.Specifier); ok {
					platforms[platform] = true
				}
			}
		}
		text := node.Text(file.Data)
		for _, rule := range riskRules {
			if rule.types != nil {
				if !rule.types[typ] {
					continue
				}
			} else if !riskTextTypes[typ] || !containsAny(text, rule.contains) {
				continue
			}
			if seenRiskLine[rule.risk] == nil {
				seenRiskLine[rule.risk] = map[int]bool{}
			}
			if !seenRiskLine[rule.risk][line] {
				seenRiskLine[rule.risk][line] = true
				facts.Risks[rule.risk] = append(facts.Risks[rule.risk], line)
			}
		}
		for i := 0; i < node.ChildCount(); i++ {
			walk(node.Child(i))
		}
	}
	walk(tree.RootNode())
	for platform := range platforms {
		facts.Platforms = append(facts.Platforms, platform)
	}
	return facts
}

func importSpecifiers(langName, typ, text string) []Import {
	switch langName {
	case "go":
		if typ != "import_spec" {
			return nil
		}
		var out []Import
		for _, match := range goImportPath.FindAllStringSubmatch(text, -1) {
			out = append(out, Import{Specifier: match[1]})
		}
		return out
	case "python":
		return pythonImports(text)
	default:
		dynamic := strings.Contains(text, "import(")
		switch typ {
		case "import_statement":
		case "export_statement":
			if !strings.Contains(text, "from") {
				return nil
			}
		case "call_expression":
			if !dynamic {
				return nil
			}
		default:
			return nil
		}
		var out []Import
		for _, match := range quoted.FindAllStringSubmatch(text, -1) {
			out = append(out, Import{
				Specifier: match[1],
				TypeOnly:  strings.Contains(text, "import type"),
				Dynamic:   dynamic,
			})
		}
		return out
	}
}

func pythonImports(text string) []Import {
	trimmed := strings.TrimSpace(text)
	var out []Import
	switch {
	case strings.HasPrefix(trimmed, "import "):
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "import "))
		for _, part := range strings.Split(rest, ",") {
			fields := strings.Fields(strings.TrimSpace(part))
			if len(fields) > 0 {
				out = append(out, Import{Specifier: fields[0]})
			}
		}
	case strings.HasPrefix(trimmed, "from "):
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "from "))
		if index := strings.Index(rest, " import"); index >= 0 {
			rest = rest[:index]
		}
		fields := strings.Fields(rest)
		if len(fields) > 0 {
			out = append(out, Import{Specifier: fields[0]})
		}
	}
	return out
}

func containsAny(text string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func platformOf(langName, spec string) (Platform, bool) {
	if langName != "typescript" && langName != "tsx" && langName != "javascript" {
		return "", false
	}
	root := spec
	if index := strings.IndexByte(root, '/'); index >= 0 {
		root = root[:index]
	}
	switch root {
	case "fs", "path", "http", "https", "crypto", "os", "stream", "util", "events", "child_process", "worker_threads", "net", "url", "zlib":
		return PlatformNode, true
	case "react", "react-dom", "vue", "svelte", "solid-js", "preact", "@vitejs":
		return PlatformUI, true
	case "vscode", "@types/vscode":
		return PlatformEditor, true
	}
	return "", false
}

func countLOC(source []byte, langName string) int {
	lines := strings.Split(string(source), "\n")
	prefixes := []string{"//", "/*", "*", "*/"}
	switch strings.ToLower(langName) {
	case "python":
		prefixes = []string{"#"}
	case "powershell":
		prefixes = []string{"#", "<#"}
	}
	count := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		comment := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(trimmed, prefix) {
				comment = true
				break
			}
		}
		if !comment {
			count++
		}
	}
	return count
}
