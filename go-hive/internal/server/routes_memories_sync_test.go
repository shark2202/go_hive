package server

// Parity: core/framework/server/routes_memories.py::_maybe_sync_memory

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
)

func newMemorySyncTestMux(home hivehome.Home, pushes *[]string) *http.ServeMux {
	mux := http.NewServeMux()
	deps := MemoryRouteDeps{Home: home}
	if pushes != nil {
		deps.SchedulePush = func(kind, key string) {
			*pushes = append(*pushes, kind+"|"+key)
		}
	}
	RegisterMemoryRoutes(mux, deps)
	return mux
}

// memoryWriteRoundTrip drives one successful create -> update -> delete ->
// restore sequence and returns each step's recorder and decoded body.
func memoryWriteRoundTrip(t *testing.T, mux *http.ServeMux, rel string) ([]*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	fileTarget := "/api/memories/file?path=" + url.QueryEscape(rel)
	restoreTarget := "/api/memories/restore?path=" + url.QueryEscape(rel)
	steps := []struct {
		method string
		target string
		body   string
	}{
		{http.MethodPost, fileTarget, `{"name":"Sync","body":"first"}`},
		{http.MethodPut, fileTarget, `{"content":"second"}`},
		{http.MethodDelete, fileTarget, ""},
		{http.MethodPost, restoreTarget, `{"n":2}`},
	}
	recorders := make([]*httptest.ResponseRecorder, 0, len(steps))
	bodies := make([]map[string]any, 0, len(steps))
	for _, step := range steps {
		recorder, body := memoryRequest(t, mux, step.method, step.target, step.body)
		recorders = append(recorders, recorder)
		bodies = append(bodies, body)
	}
	return recorders, bodies
}

// Parity: core/framework/server/routes_memories.py::_maybe_sync_memory
func TestMaybeSyncMemoryScopePrefixes(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{"global/x.md", true},
		{"global/nested/x.md", true},
		{"globalish/x.md", false},
		{"agents/queens/q1/x.md", true},
		{"agents/queenship/x.md", false},
		{"agents/worker/x.md", false},
		{"colonies/c1/x.md", false},
		{"root.md", false},
		{"", false},
	}
	for _, tc := range cases {
		pushes := []string{}
		deps := MemoryRouteDeps{SchedulePush: func(kind, key string) {
			pushes = append(pushes, kind+"|"+key)
		}}
		deps.maybeSyncMemory(tc.rel)
		if tc.want {
			want := []string{"memory|" + tc.rel}
			if !reflect.DeepEqual(pushes, want) {
				t.Fatalf("%q pushed %#v, want %#v", tc.rel, pushes, want)
			}
			continue
		}
		if len(pushes) != 0 {
			t.Fatalf("%q pushed %#v, want none", tc.rel, pushes)
		}
	}
}

// Parity: core/framework/server/routes_memories.py::_maybe_sync_memory
func TestMemoryWritesSchedulePushForGlobalAndQueenScopes(t *testing.T) {
	for _, rel := range []string{"global/new.md", "agents/queens/q1/memory.md"} {
		rel := rel
		t.Run(rel, func(t *testing.T) {
			home := hivehome.Home{Root: t.TempDir()}
			pushes := []string{}
			mux := newMemorySyncTestMux(home, &pushes)
			recorders, bodies := memoryWriteRoundTrip(t, mux, rel)
			for index, recorder := range recorders {
				if recorder.Code != http.StatusOK {
					t.Fatalf("step %d status = %d, body = %s", index, recorder.Code, recorder.Body.String())
				}
			}
			if bodies[0]["created"] != true || bodies[0]["path"] != rel {
				t.Fatalf("create body = %#v", bodies[0])
			}
			if bodies[2]["deleted"] != rel {
				t.Fatalf("delete body = %#v", bodies[2])
			}
			if content, _ := bodies[3]["content"].(string); content == "" {
				t.Fatalf("restore body = %#v", bodies[3])
			}
			want := []string{"memory|" + rel, "memory|" + rel, "memory|" + rel, "memory|" + rel}
			if !reflect.DeepEqual(pushes, want) {
				t.Fatalf("pushed = %#v, want %#v", pushes, want)
			}
		})
	}
}

// Parity: core/framework/server/routes_memories.py::_maybe_sync_memory
// Upstream's create handler returns {"created": false} before _maybe_sync_memory;
// an idempotent hit must not schedule a push.
func TestMemoryCreateExistingFileDoesNotPush(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	pushes := []string{}
	mux := newMemorySyncTestMux(home, &pushes)
	payload := `{"name":"Once","body":"body"}`
	if recorder, _ := memoryRequest(t, mux, http.MethodPost, "/api/memories/file?path=global%2Fonce.md", payload); recorder.Code != http.StatusOK {
		t.Fatalf("first create status = %d", recorder.Code)
	}
	recorder, body := memoryRequest(t, mux, http.MethodPost, "/api/memories/file?path=global%2Fonce.md", payload)
	if recorder.Code != http.StatusOK || body["created"] != false {
		t.Fatalf("second create = %d %#v", recorder.Code, body)
	}
	want := []string{"memory|global/once.md"}
	if !reflect.DeepEqual(pushes, want) {
		t.Fatalf("pushed = %#v, want %#v", pushes, want)
	}
}

// Parity: core/framework/server/routes_memories.py::_maybe_sync_memory
func TestMemoryWritesSkipPushForOtherScopes(t *testing.T) {
	for _, rel := range []string{"root.md", "colonies/c1/notes.md", "agents/worker/w.md"} {
		rel := rel
		t.Run(rel, func(t *testing.T) {
			home := hivehome.Home{Root: t.TempDir()}
			pushes := []string{}
			mux := newMemorySyncTestMux(home, &pushes)
			recorders, _ := memoryWriteRoundTrip(t, mux, rel)
			for index, recorder := range recorders {
				if recorder.Code != http.StatusOK {
					t.Fatalf("step %d status = %d, body = %s", index, recorder.Code, recorder.Body.String())
				}
			}
			if len(pushes) != 0 {
				t.Fatalf("%s pushed = %#v, want none", rel, pushes)
			}
		})
	}
}

// Parity: core/framework/server/routes_memories.py::_maybe_sync_memory
// A request that fails before or during the local write schedules nothing.
func TestMemoryPushSkipsFailedRequests(t *testing.T) {
	t.Run("invalid path", func(t *testing.T) {
		home := hivehome.Home{Root: t.TempDir()}
		pushes := []string{}
		mux := newMemorySyncTestMux(home, &pushes)
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
			recorder, body := memoryRequest(t, mux, method, "/api/memories/file?path=..%2Fsecret.md", `{"name":"x","body":"y","content":"y"}`)
			if recorder.Code != http.StatusBadRequest || body["error"] != "Invalid path" {
				t.Fatalf("%s unsafe = %d %#v", method, recorder.Code, body)
			}
		}
		recorder, body := memoryRequest(t, mux, http.MethodPost, "/api/memories/restore?path=..%2Fsecret.md", `{"n":1}`)
		if recorder.Code != http.StatusBadRequest || body["error"] != "Invalid path" {
			t.Fatalf("restore unsafe = %d %#v", recorder.Code, body)
		}
		if len(pushes) != 0 {
			t.Fatalf("pushed = %#v, want none", pushes)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		home := hivehome.Home{Root: t.TempDir()}
		pushes := []string{}
		mux := newMemorySyncTestMux(home, &pushes)
		recorder, body := memoryRequest(t, mux, http.MethodPut, "/api/memories/file?path=global%2Fmissing.md", `{"content":"x"}`)
		if recorder.Code != http.StatusNotFound || body["error"] != "Memory not found" {
			t.Fatalf("update missing = %d %#v", recorder.Code, body)
		}
		recorder, body = memoryRequest(t, mux, http.MethodDelete, "/api/memories/file?path=global%2Fmissing.md", "")
		if recorder.Code != http.StatusNotFound || body["error"] != "Memory not found" {
			t.Fatalf("delete missing = %d %#v", recorder.Code, body)
		}
		if len(pushes) != 0 {
			t.Fatalf("pushed = %#v, want none", pushes)
		}
	})

	t.Run("restore without that change", func(t *testing.T) {
		home := hivehome.Home{Root: t.TempDir()}
		writeMemoryFixture(t, home, "global/fixed.md", "---\nname: Fixed\n---\nbody", time.Now())
		pushes := []string{}
		mux := newMemorySyncTestMux(home, &pushes)
		recorder, body := memoryRequest(t, mux, http.MethodPost, "/api/memories/restore?path=global%2Ffixed.md", `{"n":1}`)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("restore miss = %d %#v", recorder.Code, body)
		}
		if len(pushes) != 0 {
			t.Fatalf("pushed = %#v, want none", pushes)
		}
	})

	t.Run("create write error", func(t *testing.T) {
		home := hivehome.Home{Root: t.TempDir()}
		if err := os.MkdirAll(home.MemoriesDir(), 0o755); err != nil {
			t.Fatalf("mkdir memories: %v", err)
		}
		if err := os.WriteFile(filepath.Join(home.MemoriesDir(), "global"), []byte("not a dir"), 0o644); err != nil {
			t.Fatalf("block global dir: %v", err)
		}
		pushes := []string{}
		mux := newMemorySyncTestMux(home, &pushes)
		recorder, body := memoryRequest(t, mux, http.MethodPost, "/api/memories/file?path=global%2Fnew.md", `{"name":"x","body":"y"}`)
		if recorder.Code != http.StatusInternalServerError || body["error"] != "Could not create memory" {
			t.Fatalf("create error = %d %#v", recorder.Code, body)
		}
		if len(pushes) != 0 {
			t.Fatalf("pushed = %#v, want none", pushes)
		}
	})

	t.Run("update log write error", func(t *testing.T) {
		home := hivehome.Home{Root: t.TempDir()}
		globalDir := filepath.Join(home.MemoriesDir(), "global")
		writeMemoryFixture(t, home, "global/ok.md", "---\nname: Ok\n---\nold\n", time.Now())
		if err := os.Mkdir(filepath.Join(globalDir, memory.LogFile), 0o755); err != nil {
			t.Fatalf("block log: %v", err)
		}
		pushes := []string{}
		mux := newMemorySyncTestMux(home, &pushes)
		recorder, body := memoryRequest(t, mux, http.MethodPut, "/api/memories/file?path=global%2Fok.md", `{"content":"new"}`)
		if recorder.Code != http.StatusInternalServerError || body["error"] != "Could not write memory" {
			t.Fatalf("update error = %d %#v", recorder.Code, body)
		}
		if len(pushes) != 0 {
			t.Fatalf("pushed = %#v, want none", pushes)
		}
	})

	t.Run("delete log write error", func(t *testing.T) {
		home := hivehome.Home{Root: t.TempDir()}
		globalDir := filepath.Join(home.MemoriesDir(), "global")
		writeMemoryFixture(t, home, "global/gone.md", "---\nname: Gone\n---\nbody", time.Now())
		if err := os.Mkdir(filepath.Join(globalDir, memory.LogFile), 0o755); err != nil {
			t.Fatalf("block log: %v", err)
		}
		pushes := []string{}
		mux := newMemorySyncTestMux(home, &pushes)
		recorder, body := memoryRequest(t, mux, http.MethodDelete, "/api/memories/file?path=global%2Fgone.md", "")
		if recorder.Code != http.StatusInternalServerError || body["error"] != "Could not delete memory" {
			t.Fatalf("delete error = %d %#v", recorder.Code, body)
		}
		if len(pushes) != 0 {
			t.Fatalf("pushed = %#v, want none", pushes)
		}
	})

	t.Run("restore write error", func(t *testing.T) {
		home := hivehome.Home{Root: t.TempDir()}
		globalDir := filepath.Join(home.MemoriesDir(), "global")
		if _, err := memory.Write(globalDir, "restore.md", "---\nname: Restore\n---\nfirst\n", memory.WriteOptions{
			Author: memory.AuthorOwner,
			Reason: "seed",
		}); err != nil {
			t.Fatalf("seed history: %v", err)
		}
		target := filepath.Join(globalDir, "restore.md")
		if err := os.Remove(target); err != nil {
			t.Fatalf("remove seeded file: %v", err)
		}
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatalf("block restore target: %v", err)
		}
		pushes := []string{}
		mux := newMemorySyncTestMux(home, &pushes)
		recorder, body := memoryRequest(t, mux, http.MethodPost, "/api/memories/restore?path=global%2Frestore.md", `{"n":0}`)
		if recorder.Code != http.StatusInternalServerError || body["error"] != internalServerError {
			t.Fatalf("restore error = %d %#v", recorder.Code, body)
		}
		if len(pushes) != 0 {
			t.Fatalf("pushed = %#v, want none", pushes)
		}
	})
}

// Parity: core/framework/server/routes_memories.py::_maybe_sync_memory
// With no cloud-sync reconciler wired the hook is absent: every write still
// succeeds and nothing panics.
func TestMemoryPushNilHookIsHarmless(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := newMemoryTestMux(home)
	recorders, bodies := memoryWriteRoundTrip(t, mux, "global/nohook.md")
	for index, recorder := range recorders {
		if recorder.Code != http.StatusOK {
			t.Fatalf("step %d status = %d, body = %s", index, recorder.Code, recorder.Body.String())
		}
	}
	if bodies[0]["created"] != true || bodies[2]["deleted"] != "global/nohook.md" {
		t.Fatalf("bodies = %#v", bodies)
	}
}

// Parity: core/framework/server/routes_memories.py::_maybe_sync_memory
// Upstream's schedule_push never raises and the handler does not await it;
// a panicking hook must leave the response untouched.
func TestMemoryPushHookPanicCannotFailTheRequest(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := http.NewServeMux()
	RegisterMemoryRoutes(mux, MemoryRouteDeps{
		Home: home,
		SchedulePush: func(kind, key string) {
			panic("cloud sync is down")
		},
	})
	recorders, bodies := memoryWriteRoundTrip(t, mux, "global/panic.md")
	for index, recorder := range recorders {
		if recorder.Code != http.StatusOK {
			t.Fatalf("step %d status = %d, body = %s", index, recorder.Code, recorder.Body.String())
		}
	}
	if bodies[0]["created"] != true || bodies[0]["path"] != "global/panic.md" {
		t.Fatalf("create body = %#v", bodies[0])
	}
	if bodies[2]["deleted"] != "global/panic.md" {
		t.Fatalf("delete body = %#v", bodies[2])
	}
	if content, _ := bodies[3]["content"].(string); content == "" {
		t.Fatalf("restore body = %#v", bodies[3])
	}

	recorder, body := memoryRequest(t, mux, http.MethodGet, "/api/memories/file?path=global%2Fpanic.md", "")
	if recorder.Code != http.StatusOK || body["path"] != "global/panic.md" {
		t.Fatalf("read after panicking hook = %d %#v", recorder.Code, body)
	}
}

// Parity: core/framework/server/routes_memories.py::_maybe_sync_memory
// Two-sided regression for the cloud-sync seam. The production wiring passes no
// SchedulePush at all (app.go), and that matches upstream in this checkout:
// cloud_sync_hooks.schedule_push imports framework.cloud_sync, which is not
// shipped here, so its ImportError is swallowed and every call is a no-op.
// With a reconciler wired, the same write pushes exactly once per route call.
func TestMemoryPushProductionWiringMatchesUpstreamNoOp(t *testing.T) {
	write := func(t *testing.T, deps MemoryRouteDeps, rel string) map[string]any {
		t.Helper()
		mux := http.NewServeMux()
		RegisterMemoryRoutes(mux, deps)
		recorder, body := memoryRequest(t, mux, http.MethodPost, "/api/memories/file?path="+url.QueryEscape(rel),
			`{"name":"Production","body":"written locally"}`)
		if recorder.Code != http.StatusOK || body["created"] != true {
			t.Fatalf("create = %d %#v", recorder.Code, body)
		}
		if _, err := os.Stat(filepath.Join(deps.Home.MemoriesDir(), filepath.FromSlash(rel))); err != nil {
			t.Fatalf("local write missing: %v", err)
		}
		return body
	}

	// Production shape: nothing to schedule onto, and the write is unaffected.
	production := hivehome.Home{Root: t.TempDir()}
	write(t, MemoryRouteDeps{Home: production}, "global/production.md")

	// The other side: a wired hook sees exactly the one push the write implies.
	pushed := []string{}
	wired := hivehome.Home{Root: t.TempDir()}
	write(t, MemoryRouteDeps{Home: wired, SchedulePush: func(kind, key string) {
		pushed = append(pushed, kind+"|"+key)
	}}, "global/production.md")
	want := []string{"memory|global/production.md"}
	if !reflect.DeepEqual(pushed, want) {
		t.Fatalf("pushed = %#v, want %#v", pushed, want)
	}
}
