package server

// Parity: core/framework/server/streamtoken_refresh.py::start_streamtoken_refresh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

type streamTokenTestStore struct {
	mu         sync.Mutex
	credential *credentials.CredentialObject
	saves      int
	saved      chan struct{}
	savedOnce  sync.Once
}

func newStreamTokenTestStore(token string) *streamTokenTestStore {
	store := &streamTokenTestStore{saved: make(chan struct{})}
	credential := credentials.NewCredentialObject("hive")
	credential.SetKey("api_key", token, nil, nil)
	store.credential = credential
	return store
}

func (s *streamTokenTestStore) GetCredential(credentialID string, _ bool) (*credentials.CredentialObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.credential == nil || s.credential.ID != credentialID {
		return nil, nil
	}
	return s.credential, nil
}

func (s *streamTokenTestStore) SaveCredential(credential *credentials.CredentialObject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	s.credential = credential
	s.savedOnce.Do(func() { close(s.saved) })
	return nil
}

func (s *streamTokenTestStore) token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.credential == nil {
		return ""
	}
	value, _ := s.credential.GetKeyValue("api_key")
	return value
}

func streamTokenJWT(t *testing.T, exp time.Time) string {
	t.Helper()
	encode := func(value any) string {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal jwt: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(payload)
	}
	return encode(map[string]any{"alg": "HS256", "typ": "JWT"}) + "." +
		encode(map[string]any{"sub": "u@e.com", "exp": exp.Unix()}) + ".sig"
}

// Parity: core/framework/server/streamtoken_refresh.py::_refresh_loop
// (no HIVE_CLOUD_BASE -> the loop never starts)
func TestStartStreamTokenRefreshDisabledWithoutCloudBase(t *testing.T) {
	t.Setenv("HIVE_CLOUD_BASE", "")
	llm.SetStreamTokenRefresher(nil)
	t.Cleanup(func() { llm.SetStreamTokenRefresher(nil) })

	store := newStreamTokenTestStore(streamTokenJWT(t, time.Now().Add(-time.Hour)))
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		posts.Add(1)
	}))
	defer server.Close()

	stop := StartStreamTokenRefresh(StreamTokenRefreshDeps{
		Home:  hivehome.Home{},
		Store: store,
		Log:   func(string, ...any) {},
	})
	stop()
	stop() // idempotent

	if posts.Load() != 0 {
		t.Fatalf("refresh posts = %d, want 0 outside cloud mode", posts.Load())
	}
	if token, ok := llm.RefreshStreamToken(context.Background()); ok || token != "" {
		t.Fatalf("llm seam = (%q, %v), want inert outside cloud mode", token, ok)
	}
}

// Parity: core/framework/server/streamtoken_refresh.py::_refresh_loop
// (a runtime with no credential store host simply never rotates)
func TestStartStreamTokenRefreshWithoutStoreOrHome(t *testing.T) {
	t.Setenv("HIVE_CLOUD_BASE", "https://cloud.example.test")
	llm.SetStreamTokenRefresher(nil)
	t.Cleanup(func() { llm.SetStreamTokenRefresher(nil) })

	var logs []string
	stop := StartStreamTokenRefresh(StreamTokenRefreshDeps{
		Log: func(format string, args ...any) {
			logs = append(logs, format)
		},
	})
	stop()
	if len(logs) == 0 {
		t.Fatal("expected a disabled log line")
	}
	if token, ok := llm.RefreshStreamToken(context.Background()); ok || token != "" {
		t.Fatalf("llm seam = (%q, %v), want inert without a store", token, ok)
	}
}

// Parity: core/framework/server/streamtoken_refresh.py::start_streamtoken_refresh
func TestStartStreamTokenRefreshRotatesAndServesLLMSeam(t *testing.T) {
	oldToken := streamTokenJWT(t, time.Now().Add(time.Hour))
	newToken := streamTokenJWT(t, time.Now().Add(24*time.Hour))
	store := newStreamTokenTestStore(oldToken)
	var posts atomic.Int32
	var presented atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		posts.Add(1)
		var body struct {
			StreamToken string `json:"streamToken"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		presented.Store(body.StreamToken)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"success": true, "streamToken": newToken})
	}))
	defer server.Close()

	t.Setenv("HIVE_CLOUD_BASE", server.URL)
	llm.SetStreamTokenRefresher(nil)
	t.Cleanup(func() { llm.SetStreamTokenRefresher(nil) })

	var sleeps atomic.Int32
	stop := StartStreamTokenRefresh(StreamTokenRefreshDeps{
		Home:  hivehome.Home{},
		Store: store,
		Log:   func(string, ...any) {},
		// First wait returns immediately so the first tick refreshes; later
		// waits park until stop cancels the loop.
		Sleep: func(ctx context.Context, _ time.Duration) error {
			if sleeps.Add(1) == 1 {
				return nil
			}
			<-ctx.Done()
			return ctx.Err()
		},
	})

	select {
	case <-store.saved:
	case <-time.After(5 * time.Second):
		stop()
		t.Fatal("rotation loop did not persist a refreshed token")
	}

	if got := store.token(); got != newToken {
		stop()
		t.Fatalf("stored token = %q, want the rotated token", got)
	}
	if posts.Load() != 1 {
		t.Fatalf("refresh posts = %d, want 1", posts.Load())
	}
	if sent, _ := presented.Load().(string); sent != oldToken {
		t.Fatalf("refresh request presented %q, want the stored token", sent)
	}
	// The seam the LLM call site consults must hand back the rotated token
	// without another POST (it is comfortably fresh).
	seamToken, ok := llm.RefreshStreamToken(context.Background())
	if !ok || seamToken != newToken {
		stop()
		t.Fatalf("llm seam = (%q, %v), want (%q, true)", seamToken, ok, newToken)
	}
	if posts.Load() != 1 {
		stop()
		t.Fatalf("refresh posts after seam = %d, want 1", posts.Load())
	}

	stop()
	stop()
	if token, ok := llm.RefreshStreamToken(context.Background()); ok || token != "" {
		t.Fatalf("llm seam = (%q, %v), want uninstalled after stop", token, ok)
	}
}
