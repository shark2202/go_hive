package server

import (
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// Tests for the bounded pool behind upstream's _history_read_executor: the env
// table, the concurrency bound and the route's failure mode.

func withUnset(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "x")
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unsetenv %s: %v", name, err)
	}
}

// Parity: framework/server/routes_sessions.py::_history_read_executor
func TestHistoryReadWorkersResolvesLikeUpstream(t *testing.T) {
	cases := []struct {
		name  string
		value string
		set   bool
		want  int
	}{
		{"unset default", "", false, 8},
		{"override", "12", true, 12},
		{"floor at four", "2", true, 4},
		{"zero floors", "0", true, 4},
		{"negative floors", "-5", true, 4},
		{"python int form", "1_0", true, 10},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.set {
				t.Setenv("HIVE_HISTORY_READ_WORKERS", testCase.value)
			} else {
				withUnset(t, "HIVE_HISTORY_READ_WORKERS")
			}
			resetHistoryReadPoolForTest()
			t.Cleanup(resetHistoryReadPoolForTest)
			got, err := HistoryReadWorkers()
			if err != nil {
				t.Fatalf("HistoryReadWorkers: %v", err)
			}
			if got != testCase.want {
				t.Fatalf("workers = %d, want %d", got, testCase.want)
			}
		})
	}
}

// Parity: framework/server/routes_sessions.py::_history_read_executor
func TestHistoryReadWorkersGarbageIsAnError(t *testing.T) {
	t.Setenv("HIVE_HISTORY_READ_WORKERS", "many")
	resetHistoryReadPoolForTest()
	t.Cleanup(resetHistoryReadPoolForTest)
	if _, err := HistoryReadWorkers(); err == nil {
		t.Fatal("garbage pool size should raise (upstream int() has no except here), got nil error")
	}
}

// Parity: framework/server/routes_sessions.py::_history_read_executor
func TestHistoryReadPoolBoundsConcurrentDiskReads(t *testing.T) {
	t.Setenv("HIVE_HISTORY_READ_WORKERS", "6")
	resetHistoryReadPoolForTest()
	t.Cleanup(resetHistoryReadPoolForTest)

	var mu sync.Mutex
	active, maxActive := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = withHistoryReadSlot(func() {
				mu.Lock()
				active++
				if active > maxActive {
					maxActive = active
				}
				mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				mu.Lock()
				active--
				mu.Unlock()
			})
		}()
	}
	wg.Wait()
	if maxActive != 6 {
		t.Fatalf("max concurrent reads = %d, want exactly the configured 6", maxActive)
	}
}

// Parity: framework/server/routes_sessions.py::_history_read_executor
func TestSessionHistoryRouteReflectsThePoolSize(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	t.Setenv("HIVE_HISTORY_READ_WORKERS", "nope")
	resetHistoryReadPoolForTest()
	t.Cleanup(resetHistoryReadPoolForTest)

	recorder, body := doSessionJSON(t, sessionRoutesMux(SessionRouteDeps{Home: home}), http.MethodGet, "/api/sessions/history", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (upstream's int() raises out of the handler)", recorder.Code)
	}
	if _, ok := body["error"].(string); !ok {
		t.Fatalf("body = %#v, want an error message", body)
	}
}

// Parity: core/tests/test_session_listing_order.py::test_list_cold_sessions_is_strict_newest_created_first, core/tests/test_session_fork.py::test_fork_route_returns_the_new_session_which_history_lists
func TestSessionHistoryRouteReadsColdSessionsThroughThePool(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	withUnset(t, "HIVE_HISTORY_READ_WORKERS")
	resetHistoryReadPoolForTest()
	t.Cleanup(resetHistoryReadPoolForTest)

	recorder, body := doSessionJSON(t, sessionRoutesMux(SessionRouteDeps{Home: home}), http.MethodGet, "/api/sessions/history", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if _, ok := body["sessions"]; !ok {
		t.Fatalf("body = %#v, want a sessions list", body)
	}
}
