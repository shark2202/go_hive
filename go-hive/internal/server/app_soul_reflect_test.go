package server

// Parity-exempt: serve-assembly check. Before the assembly handed
// AppDeps.SoulReflect through, POST /api/queen/{id}/soul/reflect answered 503
// on every real server; this pins the wired path (202 + the queen's own soul
// config reaching the dep) and the unwired path (503, no fake run).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	soulstore "github.com/aden-hive/hive-go/internal/soul"
)

func doAppJSON(t *testing.T, app *App, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	return recorder
}

// Parity-exempt: serve assembly; no upstream test mounts the full app without a model, and the
// port must hand AppDeps.SoulReflect through instead of answering the old unwired 503.
func TestAppSoulReflectHandsTheDepThrough(t *testing.T) {
	home := soulTestHome(t)
	configs := make(chan *soulstore.SoulConfig, 1)
	app := newAppForTest(t, AppDeps{
		Home: home,
		SoulReflect: func(queenID string, cfg *soulstore.SoulConfig, bootstrap bool) error {
			configs <- cfg
			return nil
		},
	})

	recorder := doAppJSON(t, app, http.MethodPost, "/api/queen/queen_x/soul/reflect", "{}")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("wired reflect status = %d, want 202; body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"started":true`) {
		t.Fatalf("wired reflect body = %s", recorder.Body.String())
	}
	select {
	case cfg := <-configs:
		if cfg == nil || cfg.Mode != "review" {
			t.Fatalf("dep config = %#v, want the queen's review config", cfg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SoulReflect dep was never called")
	}

	recorder = doAppJSON(t, app, http.MethodPost, "/api/queen/queen_plain/soul/reflect", "{}")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("queen without soul status = %d, want 409; body = %s", recorder.Code, recorder.Body.String())
	}
}

// Parity-exempt: unwired assembly keeps an explicit 503 rather than a fake run.
func TestAppSoulReflectWithoutDepAnswers503(t *testing.T) {
	app := newAppForTest(t, AppDeps{Home: soulTestHome(t)})

	recorder := doAppJSON(t, app, http.MethodPost, "/api/queen/queen_x/soul/reflect", "{}")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired reflect status = %d, want 503; body = %s", recorder.Code, recorder.Body.String())
	}
}
