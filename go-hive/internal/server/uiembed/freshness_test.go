package uiembed

// The staged dashboard is only as fresh as the last frontend build. The slot
// binaries built before 20261122-16 embedded a dashboard older than the
// frontend sources, so Settings → Memory (the 1.0.15 memory surface) existed
// in the sources but not in the running UI. These tests pin embed_ui.sh's
// freshness decision so release tooling cannot quietly embed a stale
// dashboard again.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Parity-exempt: embed_ui.sh 的前端新鲜度判定没有 Python 对应物；这是 Go
// 侧发布卫生护栏（旧 dashboard 被嵌入 slot 二进制）。
func TestEmbedUIStalenessDecision(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("embed_ui.sh is a POSIX shell script")
	}
	script := filepath.Join("..", "..", "..", "..", "scripts", "embed_ui.sh")
	if _, err := os.Stat(script); err != nil {
		t.Skipf("embed_ui.sh is not beside this checkout: %v", err)
	}

	now := time.Now().Add(-time.Minute).Truncate(time.Second)
	cases := []struct {
		name    string
		dist    bool
		srcAge  time.Duration
		distAge time.Duration
		force   bool
		want    string
	}{
		{name: "dist missing", want: "needs-build"},
		{name: "dist newer than sources", dist: true, srcAge: -2 * time.Hour, want: "up-to-date"},
		{name: "sources newer than dist", dist: true, srcAge: 0, distAge: -2 * time.Hour, want: "needs-build"},
		{name: "forced rebuild", dist: true, srcAge: -2 * time.Hour, force: true, want: "needs-build"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frontend := t.TempDir()
			writeFileAt(t, filepath.Join(frontend, "package.json"), "{}\n", now.Add(tc.distAge))
			writeFileAt(t, filepath.Join(frontend, "src", "app.ts"), "export const a = 1\n", now.Add(tc.srcAge))
			dist := filepath.Join(frontend, "dist")
			if tc.dist {
				writeFileAt(t, filepath.Join(dist, "index.html"), "<html></html>\n", now.Add(tc.distAge))
			}

			env := embedUIEnv("FRONTEND_DIR="+frontend, "FRONTEND_DIST="+dist)
			if tc.force {
				env = append(env, "FORCE_FRONTEND_BUILD=1")
			}
			cmd := exec.Command("bash", script, "--print-mode")
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("embed_ui.sh --print-mode: %v\n%s", err, out)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("decision = %q, want %q", got, tc.want)
			}
		})
	}
}

// embedUIEnv is os.Environ without the knobs under test, plus extra entries,
// so an ambient FRONTEND_DIST/FORCE_FRONTEND_BUILD cannot mask the fixture.
func embedUIEnv(extra ...string) []string {
	blocked := map[string]bool{
		"FRONTEND_DIR":         true,
		"FRONTEND_DIST":        true,
		"FORCE_FRONTEND_BUILD": true,
	}
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 && blocked[kv[:i]] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

func writeFileAt(t *testing.T, path, body string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("touch %s: %v", path, err)
	}
}
