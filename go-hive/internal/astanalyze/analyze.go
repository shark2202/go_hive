package astanalyze

import (
	"context"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type unitBuild struct {
	id            string
	pkg           string
	files         []sourceFile
	loc           int
	loops         int
	risks         map[Risk]map[string]bool
	platforms     map[Platform]bool
	deps          map[string]bool
	workspaceDeps map[string]bool
	externalDeps  map[string]bool
	directTests   int
	fanIn         int
	parseErrors   int
}

// Analyze runs one repository analysis rooted at root (the session sandbox
// root). path must be absolute and inside root; omitted means root.
func Analyze(ctx context.Context, cfg Config, root string, req Request) (Report, error) {
	report := Report{Schema: Schema, Language: "mixed"}
	if !cfg.Enabled {
		return report, &Error{Kind: ErrConfigDisabled, Message: "ast_analyze is disabled by configuration"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	includeTests := cfg.IncludeTests
	if req.IncludeTests != nil {
		includeTests = *req.IncludeTests
	}
	includeUI := cfg.IncludeUI
	if req.IncludeUI != nil {
		includeUI = *req.IncludeUI
	}
	if req.MaxFiles > 0 {
		cfg.MaxFiles = req.MaxFiles
	}
	if req.MaxFileBytes > 0 {
		cfg.MaxFileBytes = req.MaxFileBytes
	}
	if req.MaxBytes > 0 {
		cfg.MaxReportBytes = req.MaxBytes
	}
	files, warnings, err := collectFiles(ctx, cfg, root, req.Path, req.Lang, includeTests, includeUI)
	if err != nil {
		return report, err
	}
	report.Warnings = append(report.Warnings, warnings...)

	facts, err := scanAllFacts(ctx, cfg, files)
	if err != nil {
		return report, err
	}
	langs := map[string]bool{}
	factByPath := make(map[string]int, len(files))
	for index, file := range files {
		factByPath[file.Rel] = index
		langs[file.Lang.Name()] = true
		if facts[index].ParseErrors > 0 {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %d parse error(s)", file.Rel, facts[index].ParseErrors))
		}
	}
	if len(langs) == 1 {
		for lang := range langs {
			report.Language = lang
		}
	}
	if req.Lang != "" {
		report.Language = strings.ToLower(strings.TrimSpace(req.Lang))
	}

	module := goModule(root)
	unitOfFile := map[string]string{}
	units := map[string]*unitBuild{}
	var unitOrder []string
	for i, file := range files {
		if file.Kind == FileTest {
			continue
		}
		id := unitIDFor(file.Rel)
		unit := units[id]
		if unit == nil {
			unit = &unitBuild{
				id:            id,
				pkg:           packageNameFor(file.Rel),
				risks:         map[Risk]map[string]bool{},
				platforms:     map[Platform]bool{},
				deps:          map[string]bool{},
				workspaceDeps: map[string]bool{},
				externalDeps:  map[string]bool{},
			}
			units[id] = unit
			unitOrder = append(unitOrder, id)
		}
		unit.files = append(unit.files, file)
		unit.loc += facts[i].LOC
		unit.loops += facts[i].Loops
		unit.parseErrors += facts[i].ParseErrors
		for _, platform := range facts[i].Platforms {
			unit.platforms[platform] = true
		}
		for risk, lines := range facts[i].Risks {
			if unit.risks[risk] == nil {
				unit.risks[risk] = map[string]bool{}
			}
			for _, line := range lines {
				unit.risks[risk][fmt.Sprintf("%s:%d", file.Rel, line)] = true
			}
		}
		unitOfFile[file.Rel] = id
	}
	if len(unitOrder) > cfg.MaxUnits {
		unitOrder = unitOrder[:cfg.MaxUnits]
		report.Warnings = append(report.Warnings, fmt.Sprintf("stopped after max_units (%d)", cfg.MaxUnits))
	}

	// Credit tests to the units they import.
	for i, file := range files {
		if file.Kind != FileTest {
			continue
		}
		// Same-package tests (Go, Python, TS) do not import the unit they
		// test; credit the containing unit first.
		if id := unitIDFor(file.Rel); id != "" {
			if unit := units[id]; unit != nil {
				unit.directTests++
			}
		}
		for _, imp := range facts[i].Imports {
			if id := resolveImport(module, unitOfFile, file.Rel, imp.Specifier); id != "" {
				if unit := units[id]; unit != nil {
					unit.directTests++
				}
			}
		}
	}

	// Build the unit graph.
	for _, id := range unitOrder {
		unit := units[id]
		for _, file := range unit.files {
			index, ok := factByPath[file.Rel]
			if !ok {
				continue
			}
			for _, imp := range facts[index].Imports {
				if target := resolveImport(module, unitOfFile, file.Rel, imp.Specifier); target != "" {
					if target != id {
						unit.deps[target] = true
					}
				} else if external := externalName(imp.Specifier); external != "" {
					unit.externalDeps[external] = true
				}
			}
		}
	}
	for _, id := range unitOrder {
		for dep := range units[id].deps {
			if target := units[dep]; target != nil {
				target.fanIn++
			}
		}
	}

	index := map[string]int{}
	for i, id := range unitOrder {
		index[id] = i
	}
	edges := make([][]int, len(unitOrder))
	for i, id := range unitOrder {
		for dep := range units[id].deps {
			if j, ok := index[dep]; ok {
				edges[i] = append(edges[i], j)
			}
		}
		sort.Ints(edges[i])
	}
	wave, members := waves(edges)

	reports := make([]UnitReport, 0, len(unitOrder))
	packageTests := map[string]int{}
	for _, file := range files {
		if file.Kind == FileTest {
			packageTests[packageNameFor(file.Rel)]++
		}
	}
	for i, id := range unitOrder {
		reports = append(reports, scoreUnit(units[id], wave[i], members[i], unitOrder, units, packageTests[units[id].pkg]))
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].ID < reports[j].ID })

	report.Units = reports
	report.StartHere = startHere(reports, cfg.StartHereLimit)
	report.Packages = packageReports(reports)
	report.Dependencies = dependencyReports(reports)
	report.Summary = summarize(reports, files, facts)
	if len(report.StartHere) == 0 && report.Summary.Units > 0 {
		report.Warnings = append(report.Warnings, "no ready unit with at least 20 lines and a bridgeable export")
	}
	if len(req.Focus) > 0 {
		report = filterFocus(report, req.Focus)
	}
	return report, nil
}

// scanAllFacts scans every collected file with the configured worker pool.
// It cancels with ErrCancelled when ctx ends while jobs are being dispatched.
func scanAllFacts(ctx context.Context, cfg Config, files []sourceFile) ([]FileFacts, error) {
	facts := make([]FileFacts, len(files))
	workers := cfg.Workers
	if workers < 1 {
		workers = 1
	}
	if workers > len(files) {
		workers = len(files)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				facts[index] = scanFacts(ctx, cfg, files[index])
			}
		}()
	}
	for index := range files {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return nil, &Error{Kind: ErrCancelled, Message: "ast_analyze cancelled", Err: ctx.Err()}
		}
	}
	close(jobs)
	wg.Wait()
	return facts, nil
}

func unitIDFor(rel string) string {
	dir := path.Dir(rel)
	if dir == "." {
		return rel
	}
	return strings.TrimSuffix(dir, "/") + "/"
}

func packageNameFor(rel string) string {
	parts := strings.Split(rel, "/")
	if len(parts) <= 1 {
		return "root"
	}
	return parts[0]
}

func scoreUnit(unit *unitBuild, wave int, members []int, order []string, units map[string]*unitBuild, packageTests int) UnitReport {
	report := UnitReport{
		ID:      unit.id,
		Package: unit.pkg,
		LOC:     unit.loc,
		Wave:    wave,
		Density: density(unit),
	}
	for _, file := range unit.files {
		report.Files = append(report.Files, file.Rel)
	}
	sort.Strings(report.Files)
	for platform := range unit.platforms {
		report.Platforms = append(report.Platforms, platform)
	}
	sort.Slice(report.Platforms, func(i, j int) bool { return report.Platforms[i] < report.Platforms[j] })
	for dep := range unit.deps {
		report.InternalDeps = append(report.InternalDeps, dep)
	}
	sort.Strings(report.InternalDeps)
	for dep := range unit.externalDeps {
		report.ExternalDeps = append(report.ExternalDeps, dep)
	}
	sort.Strings(report.ExternalDeps)
	report.DirectTests = unit.directTests
	report.FanIn = unit.fanIn
	report.Exports = 1
	report.Bridgeable = 1
	if keepUnit(unit) {
		report.Bridgeable = 0
	}

	riskPoints := 0.0
	for risk, samples := range unit.risks {
		count := len(samples)
		riskPoints += risk.Severity().Weight() * math.Log1p(float64(count))
		lines := make([]string, 0, len(samples))
		for sample := range samples {
			lines = append(lines, sample)
		}
		sort.Strings(lines)
		if len(lines) > 3 {
			lines = lines[:3]
		}
		report.Risks = append(report.Risks, RiskReport{Risk: risk, Severity: risk.Severity(), Count: count, Samples: lines})
	}
	sort.Slice(report.Risks, func(i, j int) bool {
		if report.Risks[i].Severity != report.Risks[j].Severity {
			return report.Risks[i].Severity > report.Risks[j].Severity
		}
		return report.Risks[i].Risk < report.Risks[j].Risk
	})
	testBonus := 0.0
	if unit.directTests > 0 {
		testBonus = 10
	} else if packageTests > 0 {
		testBonus = 4
	}
	readiness := 100 - riskPoints - float64(unit.loc)/40 - math.Min(float64(unit.fanIn)*1.5, 15) - float64(len(unit.externalDeps))*4 + testBonus
	readiness = math.Max(0, math.Min(100, readiness))
	report.Readiness = int(math.Round(readiness))
	report.Status = unitStatus(unit, readiness)
	report.Bridge = unitBridge(unit)
	value := math.Min(report.Density*4, 50) + func() float64 {
		if unit.directTests > 0 {
			return 30
		}
		if packageTests > 0 {
			return 15
		}
		return 0
	}() + sizeValue(unit.loc)
	report.Priority = int(math.Round(math.Max(0, math.Min(100, readiness*0.5+value*0.5))))
	if len(members) > 1 {
		others := make([]string, 0, len(members)-1)
		for _, member := range members {
			id := order[member]
			if id != unit.id {
				others = append(others, id)
			}
		}
		sort.Strings(others)
		report.Notes = append(report.Notes, fmt.Sprintf("Import cycle with %s: port them together.", strings.Join(others, ", ")))
	}
	if len(report.Risks) > 0 {
		report.Notes = append(report.Notes, fmt.Sprintf("Top risk: %s x%d.", report.Risks[0].Risk, report.Risks[0].Count))
	}
	if len(report.ExternalDeps) > 0 {
		report.Notes = append(report.Notes, fmt.Sprintf("%d external dependencies: check the Rust/Go equivalent by hand.", len(report.ExternalDeps)))
	}
	if unit.directTests > 0 {
		report.Notes = append(report.Notes, fmt.Sprintf("%d test file(s) import it directly.", unit.directTests))
	}
	return report
}

func keepUnit(unit *unitBuild) bool {
	if unit.platforms[PlatformUI] || unit.platforms[PlatformEditor] {
		return true
	}
	for _, file := range unit.files {
		if file.Kind == FileUI {
			return true
		}
	}
	return false
}

func unitStatus(unit *unitBuild, readiness float64) Status {
	if keepUnit(unit) {
		return StatusKeep
	}
	if unit.risks[RiskDynamicCode] != nil {
		return StatusBlocked
	}
	if readiness < 50 || unit.parseErrors > 0 {
		return StatusCaution
	}
	return StatusReady
}

func unitBridge(unit *unitBuild) Bridge {
	if keepUnit(unit) {
		return BridgeKeep
	}
	lang := ""
	if len(unit.files) > 0 {
		lang = strings.ToLower(unit.files[0].Lang.Name())
	}
	switch lang {
	case "go":
		return BridgeDirect
	case "c", "cpp":
		return BridgeCgo
	case "python", "typescript", "tsx", "javascript":
		return BridgeSubprocess
	default:
		return BridgeDirect
	}
}

func sizeValue(loc int) float64 {
	switch {
	case loc < 80:
		return float64(loc) / 4
	case loc < 600:
		return 20
	default:
		return 10
	}
}

func density(unit *unitBuild) float64 {
	if unit.loc == 0 {
		return 0
	}
	stringOps := len(unit.risks[RiskStringOffsets])
	return math.Round(float64(unit.loops+stringOps)*100/float64(unit.loc)*10) / 10
}

func startHere(units []UnitReport, limit int) []string {
	candidates := make([]UnitReport, 0, len(units))
	for _, unit := range units {
		if unit.Status == StatusReady && unit.LOC >= 20 && unit.Bridgeable > 0 {
			candidates = append(candidates, unit)
		}
	}
	if len(candidates) < 3 {
		candidates = candidates[:0]
		for _, unit := range units {
			if (unit.Status == StatusReady || unit.Status == StatusCaution) && unit.LOC >= 20 && unit.Bridgeable > 0 {
				candidates = append(candidates, unit)
			}
		}
	}
	if len(candidates) == 0 {
		for _, unit := range units {
			if (unit.Status == StatusReady || unit.Status == StatusCaution) && unit.Bridgeable > 0 {
				candidates = append(candidates, unit)
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority > candidates[j].Priority
		}
		if candidates[i].Readiness != candidates[j].Readiness {
			return candidates[i].Readiness > candidates[j].Readiness
		}
		if candidates[i].Wave != candidates[j].Wave {
			return candidates[i].Wave < candidates[j].Wave
		}
		if candidates[i].Density != candidates[j].Density {
			return candidates[i].Density > candidates[j].Density
		}
		return candidates[i].ID < candidates[j].ID
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]string, 0, len(candidates))
	for _, unit := range candidates {
		out = append(out, unit.ID)
	}
	return out
}

func packageReports(units []UnitReport) []PackageReport {
	byPackage := map[string]*PackageReport{}
	var names []string
	for _, unit := range units {
		pkg := byPackage[unit.Package]
		if pkg == nil {
			pkg = &PackageReport{Name: unit.Package, Dir: ".", Runtime: "portable"}
			byPackage[unit.Package] = pkg
			names = append(names, unit.Package)
		}
		pkg.Units = append(pkg.Units, unit.ID)
	}
	sort.Strings(names)
	out := make([]PackageReport, 0, len(names))
	for _, name := range names {
		pkg := byPackage[name]
		pkg.Order = len(out) + 1
		sort.Strings(pkg.Units)
		out = append(out, *pkg)
	}
	return out
}

func dependencyReports(units []UnitReport) []DependencyReport {
	used := map[string]int{}
	for _, unit := range units {
		for _, dep := range unit.ExternalDeps {
			used[dep]++
		}
	}
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]DependencyReport, 0, len(names))
	for _, name := range names {
		out = append(out, DependencyReport{Name: name, UsedBy: used[name], Fit: "unknown"})
	}
	return out
}

func summarize(units []UnitReport, files []sourceFile, facts []FileFacts) Summary {
	summary := Summary{Packages: len(packageReports(units)), Units: len(units)}
	for _, file := range files {
		summary.Files++
		if file.Kind == FileTest {
			summary.TestFiles++
		}
		if file.Kind == FileUI {
			summary.UIFiles++
		}
	}
	for _, fact := range facts {
		summary.LOC += fact.LOC
		summary.ParseErrors += fact.ParseErrors
	}
	for _, unit := range units {
		switch unit.Status {
		case StatusReady:
			summary.Ready++
		case StatusCaution:
			summary.Caution++
		case StatusBlocked:
			summary.Blocked++
		case StatusKeep:
			summary.Keep++
		}
	}
	return summary
}

func filterFocus(report Report, focus []string) Report {
	matches := func(id string) bool {
		for _, prefix := range focus {
			prefix = strings.TrimSpace(prefix)
			if prefix != "" && strings.HasPrefix(id, prefix) {
				return true
			}
		}
		return false
	}
	units := report.Units[:0]
	for _, unit := range report.Units {
		if matches(unit.ID) {
			units = append(units, unit)
		}
	}
	report.Units = units
	start := report.StartHere[:0]
	for _, id := range report.StartHere {
		if matches(id) {
			start = append(start, id)
		}
	}
	report.StartHere = start
	return report
}

// externalName reduces a specifier to its package name for the dependency table.
func externalName(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ""
	}
	if strings.HasPrefix(spec, ".") || filepath.IsAbs(spec) {
		return ""
	}
	spec = strings.TrimPrefix(spec, "node:")
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(parts[0], "@") && len(parts) > 1 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// resolveImport maps one import to a unit ID, or "" when it is external,
// stdlib, or unresolved. fileIndex is the path→unit map, so resolution never
// stats the filesystem per import.
func resolveImport(module string, fileIndex map[string]string, fromRel, spec string) string {
	lang := strings.ToLower(filepath.Ext(fromRel))
	switch lang {
	case ".go":
		return resolveGo(module, spec)
	case ".py", ".pyi":
		return resolvePython(fileIndex, fromRel, spec)
	case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
		return resolveTS(fileIndex, fromRel, spec)
	}
	return ""
}

func resolveGo(module, spec string) string {
	if module == "" || spec == module {
		return ""
	}
	if strings.HasPrefix(spec, module+"/") {
		dir := strings.TrimPrefix(spec, module+"/")
		return strings.TrimSuffix(dir, "/") + "/"
	}
	return ""
}

func resolvePython(fileIndex map[string]string, fromRel, spec string) string {
	base := path.Dir(fromRel)
	if strings.HasPrefix(spec, ".") {
		dots := 0
		for dots < len(spec) && spec[dots] == '.' {
			dots++
		}
		rest := spec[dots:]
		for i := 1; i < dots; i++ {
			base = path.Dir(base)
		}
		return pythonModule(fileIndex, path.Join(base, strings.ReplaceAll(rest, ".", "/")))
	}
	return pythonModule(fileIndex, strings.ReplaceAll(spec, ".", "/"))
}

func pythonModule(fileIndex map[string]string, module string) string {
	for _, candidate := range []string{module + ".py", module + "/__init__.py"} {
		if id, ok := fileIndex[filepath.ToSlash(candidate)]; ok {
			return id
		}
	}
	return ""
}

func resolveTS(fileIndex map[string]string, fromRel, spec string) string {
	var base string
	switch {
	case filepath.IsAbs(spec):
		base = filepath.ToSlash(filepath.Clean(spec))
	case strings.HasPrefix(spec, "."):
		base = path.Clean(path.Join(path.Dir(fromRel), spec))
	default:
		return ""
	}
	exts := []string{"", ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}
	for _, ext := range exts {
		candidate := base + ext
		if id, ok := fileIndex[candidate]; ok {
			return id
		}
	}
	for _, indexExt := range []string{".ts", ".tsx", ".js", ".jsx"} {
		if id, ok := fileIndex[path.Join(base, "index"+indexExt)]; ok {
			return id
		}
	}
	return ""
}

func goModule(root string) string {
	// #nosec G304 -- root is the session workspace root; the filename is the fixed "go.mod".
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return fields[1]
		}
	}
	return ""
}
