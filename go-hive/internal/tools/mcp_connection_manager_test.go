package tools

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type poolFakeClient struct {
	config          *MCPServerConfig
	connected       bool
	connectCalls    int
	disconnectCalls int
	listToolsCalls  int
	listToolsError  error
}

var (
	_ MCPClient            = (*poolFakeClient)(nil)
	_ mcpConnectedReporter = (*poolFakeClient)(nil)
	_ mcpListErrorReporter = (*poolFakeClient)(nil)
)

func newPoolFakeClient(config *MCPServerConfig) *poolFakeClient {
	return &poolFakeClient{config: config}
}

func (c *poolFakeClient) Connect() error {
	c.connectCalls++
	c.connected = true
	return nil
}

func (c *poolFakeClient) Disconnect() error {
	c.disconnectCalls++
	c.connected = false
	return nil
}

func (c *poolFakeClient) ListTools() []MCPTool {
	c.listToolsCalls++
	return []MCPTool{{
		Name:        "ping",
		Description: "Ping",
		InputSchema: map[string]any{"type": "object"},
		ServerName:  c.config.Name,
	}}
}

func (c *poolFakeClient) ListToolsError() error { return c.listToolsError }

func (c *poolFakeClient) CallTool(string, map[string]any) (any, error) { return nil, nil }

func (c *poolFakeClient) Config() *MCPServerConfig { return c.config }

func (c *poolFakeClient) IsConnected() bool { return c.connected }

type failingConnectClient struct{ *poolFakeClient }

func (c *failingConnectClient) Connect() error {
	c.connectCalls++
	return errors.New("connect failed")
}

type failingDisconnectClient struct{ *poolFakeClient }

func (c *failingDisconnectClient) Disconnect() error {
	c.disconnectCalls++
	c.connected = false
	return errors.New("disconnect failed")
}

type firstFailsThenWorksClient struct {
	*poolFakeClient
	attempts *atomic.Int32
}

func (c *firstFailsThenWorksClient) Disconnect() error {
	c.disconnectCalls++
	c.connected = false
	if c.attempts.Add(1) == 1 {
		return errors.New("old disconnect failed")
	}
	return nil
}

type poolClientRegistry struct {
	mu      sync.Mutex
	clients []MCPClient
}

func (r *poolClientRegistry) add(client MCPClient) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clients = append(r.clients, client)
}

func (r *poolClientRegistry) all() []MCPClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]MCPClient(nil), r.clients...)
}

func (r *poolClientRegistry) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.clients)
}

func newPoolWithFactory(registry *poolClientRegistry, build func(*MCPServerConfig) MCPClient) *MCPClientPool {
	pool := NewMCPClientPool()
	pool.NewClient = func(config *MCPServerConfig) MCPClient {
		client := build(config)
		registry.add(client)
		return client
	}
	return pool
}

func poolEntry(pool *MCPClientPool, name string) (refcount int, client MCPClient, hasClient, hasTransition, hasConfig bool) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	refcount = pool.refcounts[name]
	client, hasClient = pool.pool[name]
	_, hasTransition = pool.transitions[name]
	_, hasConfig = pool.configs[name]
	return refcount, client, hasClient, hasTransition, hasConfig
}

func poolSizes(pool *MCPClientPool) (poolSize, refcountSize, configSize, transitionSize int) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return len(pool.pool), len(pool.refcounts), len(pool.configs), len(pool.transitions)
}

func mustAcquire(t *testing.T, pool *MCPClientPool, config *MCPServerConfig) MCPClient {
	t.Helper()
	client, err := pool.Acquire(config)
	if err != nil {
		t.Fatalf("Acquire(%s): %v", config.Name, err)
	}
	return client
}

// Parity: core/tests/test_mcp_connection_manager.py::test_acquire_returns_same_client_for_same_server_name
func TestAcquireReturnsSameClientForSameServerName(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return newPoolFakeClient(config)
	})
	config := &MCPServerConfig{Name: "shared", Transport: "stdio", Command: "echo"}

	first := mustAcquire(t, pool, config)
	second := mustAcquire(t, pool, config)

	if first != second {
		t.Error("acquire returned two different clients for the same server")
	}
	if refcount, _, _, _, _ := poolEntry(pool, "shared"); refcount != 2 {
		t.Errorf("refcount = %d, want 2", refcount)
	}
	if got := registry.len(); got != 1 {
		t.Errorf("instances = %d, want 1", got)
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_release_with_refcount_above_one_keeps_connection_open
func TestReleaseWithRefcountAboveOneKeepsConnectionOpen(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return newPoolFakeClient(config)
	})
	config := &MCPServerConfig{Name: "shared", Transport: "stdio", Command: "echo"}
	client := mustAcquire(t, pool, config)
	mustAcquire(t, pool, config)

	if err := pool.Release("shared"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if fake := client.(*poolFakeClient); fake.disconnectCalls != 0 {
		t.Errorf("disconnect_calls = %d, want 0", fake.disconnectCalls)
	}
	refcount, pooled, hasClient, _, _ := poolEntry(pool, "shared")
	if !hasClient || pooled != client {
		t.Error("pooled client missing or replaced after partial release")
	}
	if refcount != 1 {
		t.Errorf("refcount = %d, want 1", refcount)
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_release_last_reference_disconnects_and_removes_from_pool
func TestReleaseLastReferenceDisconnectsAndRemovesFromPool(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return newPoolFakeClient(config)
	})
	client := mustAcquire(t, pool, &MCPServerConfig{Name: "shared", Transport: "stdio", Command: "echo"})

	if err := pool.Release("shared"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if fake := client.(*poolFakeClient); fake.disconnectCalls != 1 {
		t.Errorf("disconnect_calls = %d, want 1", fake.disconnectCalls)
	}
	if _, _, hasClient, _, _ := poolEntry(pool, "shared"); hasClient {
		t.Error("client still pooled after last release")
	}
	if _, _, _, _, hasConfig := poolEntry(pool, "shared"); hasConfig {
		t.Error("config still cached after last release")
	}
	if poolSize, refcountSize, _, _ := poolSizes(pool); poolSize != 0 || refcountSize != 0 {
		t.Errorf("pool/refcounts = %d/%d, want 0/0", poolSize, refcountSize)
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_concurrent_acquire_and_release_keeps_state_consistent
func TestConcurrentAcquireAndReleaseKeepsStateConsistent(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return newPoolFakeClient(config)
	})
	config := &MCPServerConfig{Name: "shared", Transport: "stdio", Command: "echo"}

	const workerCount = 8
	start := make(chan struct{})
	allAcquired := make(chan struct{})
	releaseGate := make(chan struct{})

	var (
		mu       sync.Mutex
		acquired []MCPClient
		errs     []error
		acquireW sync.WaitGroup
		workers  sync.WaitGroup
	)
	acquireW.Add(workerCount)
	workers.Add(workerCount)

	for i := 0; i < workerCount; i++ {
		go func() {
			defer workers.Done()
			<-start
			client, err := pool.Acquire(config)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				acquireW.Done()
				return
			}
			mu.Lock()
			acquired = append(acquired, client)
			mu.Unlock()
			acquireW.Done()

			<-allAcquired
			<-releaseGate
			_ = pool.Release("shared")
		}()
	}

	close(start)
	acquireW.Wait()
	close(allAcquired)
	close(releaseGate)
	workers.Wait()

	if len(errs) != 0 {
		t.Fatalf("acquire errors: %v", errs)
	}
	if len(acquired) != workerCount {
		t.Fatalf("acquired %d clients, want %d", len(acquired), workerCount)
	}
	for _, client := range acquired {
		if client != acquired[0] {
			t.Fatal("concurrent acquire handed out more than one client")
		}
	}
	if got := registry.len(); got != 1 {
		t.Errorf("instances = %d, want 1", got)
	}
	if fake := acquired[0].(*poolFakeClient); fake.disconnectCalls != 1 {
		t.Errorf("disconnect_calls = %d, want 1", fake.disconnectCalls)
	}
	if poolSize, refcountSize, _, _ := poolSizes(pool); poolSize != 0 || refcountSize != 0 {
		t.Errorf("pool/refcounts = %d/%d, want 0/0", poolSize, refcountSize)
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_cleanup_all_disconnects_every_pooled_client
func TestCleanupAllDisconnectsEveryPooledClient(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return newPoolFakeClient(config)
	})
	mustAcquire(t, pool, &MCPServerConfig{Name: "one", Transport: "stdio", Command: "echo"})
	mustAcquire(t, pool, &MCPServerConfig{Name: "two", Transport: "stdio", Command: "echo"})

	pool.CleanupAll()

	if got := registry.len(); got != 2 {
		t.Fatalf("instances = %d, want 2", got)
	}
	for _, client := range registry.all() {
		if fake := client.(*poolFakeClient); fake.disconnectCalls != 1 {
			t.Errorf("disconnect_calls = %d, want 1", fake.disconnectCalls)
		}
	}
	if poolSize, refcountSize, configSize, transitionSize := poolSizes(pool); poolSize != 0 || refcountSize != 0 || configSize != 0 || transitionSize != 0 {
		t.Errorf("pool/refcounts/configs/transitions = %d/%d/%d/%d, want all 0",
			poolSize, refcountSize, configSize, transitionSize)
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_reconnect_replaces_client_even_with_existing_refcount
func TestReconnectReplacesClientEvenWithExistingRefcount(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return newPoolFakeClient(config)
	})
	config := &MCPServerConfig{Name: "shared", Transport: "stdio", Command: "echo"}
	original := mustAcquire(t, pool, config)
	mustAcquire(t, pool, config)

	replacement, err := pool.Reconnect("shared")
	if err != nil {
		t.Fatalf("Reconnect: %v", err)
	}

	if replacement == original {
		t.Error("reconnect returned the original client")
	}
	if fake := original.(*poolFakeClient); fake.disconnectCalls != 1 {
		t.Errorf("original disconnect_calls = %d, want 1", fake.disconnectCalls)
	}
	refcount, pooled, hasClient, _, _ := poolEntry(pool, "shared")
	if !hasClient || pooled != replacement {
		t.Error("replacement not installed in the pool")
	}
	if refcount != 2 {
		t.Errorf("refcount = %d, want 2", refcount)
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_health_check_returns_false_when_server_is_unreachable
func TestHealthCheckReturnsFalseWhenServerIsUnreachable(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return newPoolFakeClient(config)
	})
	pool.HealthProbe = func(*MCPServerConfig) error { return errors.New("unreachable") }
	mustAcquire(t, pool, &MCPServerConfig{Name: "shared", Transport: "http", URL: "http://localhost:9"})

	if pool.HealthCheck("shared") {
		t.Error("health_check = true, want false")
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_health_check_for_stdio_returns_true_when_healthy
func TestHealthCheckForStdioReturnsTrueWhenHealthy(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return newPoolFakeClient(config)
	})
	mustAcquire(t, pool, &MCPServerConfig{Name: "shared", Transport: "stdio", Command: "echo"})

	if !pool.HealthCheck("shared") {
		t.Error("health_check = false, want true")
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_health_check_for_stdio_returns_false_on_tools_list_error
func TestHealthCheckForStdioReturnsFalseOnToolsListError(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return newPoolFakeClient(config)
	})
	client := mustAcquire(t, pool, &MCPServerConfig{Name: "shared", Transport: "stdio", Command: "echo"})
	client.(*poolFakeClient).listToolsError = errors.New("broken")

	if pool.HealthCheck("shared") {
		t.Error("health_check = true, want false")
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_health_check_for_sse_uses_list_tools
func TestHealthCheckForSSEUsesListTools(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return newPoolFakeClient(config)
	})
	client := mustAcquire(t, pool, &MCPServerConfig{
		Name: "stream", Transport: "sse", URL: "http://localhost:9000/sse",
	})

	if !pool.HealthCheck("stream") {
		t.Error("health_check = false, want true")
	}
	if fake := client.(*poolFakeClient); fake.listToolsCalls < 1 {
		t.Errorf("list_tools_calls = %d, want >= 1", fake.listToolsCalls)
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_health_check_unknown_server_returns_false
func TestHealthCheckUnknownServerReturnsFalse(t *testing.T) {
	pool := NewMCPClientPool()
	if pool.HealthCheck("nonexistent") {
		t.Error("health_check = true, want false")
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_acquire_cleans_up_transition_when_connect_fails
func TestAcquireCleansUpTransitionWhenConnectFails(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return &failingConnectClient{poolFakeClient: newPoolFakeClient(config)}
	})

	_, err := pool.Acquire(&MCPServerConfig{Name: "broken", Transport: "stdio", Command: "echo"})
	if err == nil || !strings.Contains(err.Error(), "connect failed") {
		t.Fatalf("Acquire error = %v, want connect failed", err)
	}

	if _, _, hasClient, hasTransition, _ := poolEntry(pool, "broken"); hasClient || hasTransition {
		t.Errorf("pooled/transition = %v/%v, want false/false", hasClient, hasTransition)
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_release_handles_disconnect_failure
func TestReleaseHandlesDisconnectFailure(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return &failingDisconnectClient{poolFakeClient: newPoolFakeClient(config)}
	})
	mustAcquire(t, pool, &MCPServerConfig{Name: "flaky", Transport: "stdio", Command: "echo"})

	if err := pool.Release("flaky"); err != nil {
		t.Fatalf("Release returned %v, want nil", err)
	}

	if _, _, hasClient, hasTransition, _ := poolEntry(pool, "flaky"); hasClient || hasTransition {
		t.Errorf("pooled/transition = %v/%v, want false/false", hasClient, hasTransition)
	}
	if _, _, _, _, hasConfig := poolEntry(pool, "flaky"); hasConfig {
		t.Error("config still cached after disconnect failure")
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_reconnect_handles_old_client_disconnect_failure
func TestReconnectHandlesOldClientDisconnectFailure(t *testing.T) {
	var attempts atomic.Int32
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return &firstFailsThenWorksClient{
			poolFakeClient: newPoolFakeClient(config),
			attempts:       &attempts,
		}
	})
	original := mustAcquire(t, pool, &MCPServerConfig{Name: "flaky", Transport: "stdio", Command: "echo"})

	replacement, err := pool.Reconnect("flaky")
	if err != nil {
		t.Fatalf("Reconnect: %v", err)
	}
	if replacement == original {
		t.Error("reconnect returned the original client")
	}
	_, pooled, hasClient, hasTransition, _ := poolEntry(pool, "flaky")
	if !hasClient || pooled != replacement {
		t.Error("replacement not installed in the pool")
	}
	if hasTransition {
		t.Error("transition still registered after reconnect")
	}

	pool.CleanupAll()
}

// Parity: core/tests/test_mcp_connection_manager.py::test_cleanup_all_handles_disconnect_failure
func TestCleanupAllHandlesDisconnectFailure(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return &failingDisconnectClient{poolFakeClient: newPoolFakeClient(config)}
	})
	mustAcquire(t, pool, &MCPServerConfig{Name: "a", Transport: "stdio", Command: "echo"})
	mustAcquire(t, pool, &MCPServerConfig{Name: "b", Transport: "stdio", Command: "echo"})

	pool.CleanupAll()

	if poolSize, refcountSize, _, _ := poolSizes(pool); poolSize != 0 || refcountSize != 0 {
		t.Errorf("pool/refcounts = %d/%d, want 0/0", poolSize, refcountSize)
	}
}

// Parity: core/tests/test_mcp_connection_manager.py::test_reconnect_on_fully_released_server_raises
func TestReconnectOnFullyReleasedServerRaises(t *testing.T) {
	registry := &poolClientRegistry{}
	pool := newPoolWithFactory(registry, func(config *MCPServerConfig) MCPClient {
		return newPoolFakeClient(config)
	})
	mustAcquire(t, pool, &MCPServerConfig{Name: "gone", Transport: "stdio", Command: "echo"})
	if err := pool.Release("gone"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	_, err := pool.Reconnect("gone")
	if err == nil || !strings.Contains(err.Error(), "Unknown MCP server") {
		t.Fatalf("Reconnect error = %v, want Unknown MCP server", err)
	}
}

// Parity-exempt: every upstream test reaches the manager through
func TestSharedMCPClientPoolIsProcessSingleton(t *testing.T) {
	first := SharedMCPClientPool()
	if first == nil {
		t.Fatal("SharedMCPClientPool() = nil")
	}
	if second := SharedMCPClientPool(); first != second {
		t.Error("SharedMCPClientPool returned two different pools")
	}
}
