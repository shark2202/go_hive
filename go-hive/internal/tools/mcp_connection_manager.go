package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

var mcpPoolTransitionTimeout = 30 * time.Second

const mcpHealthProbeTimeout = 5 * time.Second

type mcpPoolTransition struct {
	done chan struct{}
	once sync.Once
}

func newMCPPoolTransition() *mcpPoolTransition {
	return &mcpPoolTransition{done: make(chan struct{})}
}

func (t *mcpPoolTransition) set() { t.once.Do(func() { close(t.done) }) }

func (t *mcpPoolTransition) isSet() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

func (t *mcpPoolTransition) wait(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-t.done:
		return true
	case <-timer.C:
		return false
	}
}

type mcpListErrorReporter interface {
	ListToolsError() error
}

type mcpConnectedReporter interface {
	IsConnected() bool
}

type MCPClientPool struct {
	NewClient   MCPClientFactory
	HealthProbe func(config *MCPServerConfig) error

	mu          sync.Mutex
	pool        map[string]MCPClient
	refcounts   map[string]int
	configs     map[string]*MCPServerConfig
	transitions map[string]*mcpPoolTransition
}

var _ MCPConnectionManager = (*MCPClientPool)(nil)

func NewMCPClientPool() *MCPClientPool {
	return &MCPClientPool{
		NewClient:   NewMCPClient,
		HealthProbe: DefaultMCPHealthProbe,
		pool:        make(map[string]MCPClient),
		refcounts:   make(map[string]int),
		configs:     make(map[string]*MCPServerConfig),
		transitions: make(map[string]*mcpPoolTransition),
	}
}

var (
	sharedMCPPool   *MCPClientPool
	sharedMCPPoolMu sync.Mutex
)

func SharedMCPClientPool() *MCPClientPool {
	sharedMCPPoolMu.Lock()
	defer sharedMCPPoolMu.Unlock()
	if sharedMCPPool == nil {
		sharedMCPPool = NewMCPClientPool()
	}
	return sharedMCPPool
}

func (m *MCPClientPool) clientFactory() MCPClientFactory {
	if m.NewClient != nil {
		return m.NewClient
	}
	return NewMCPClient
}

func (m *MCPClientPool) healthProbe() func(*MCPServerConfig) error {
	if m.HealthProbe != nil {
		return m.HealthProbe
	}
	return DefaultMCPHealthProbe
}

func (m *MCPClientPool) isConnected(client MCPClient) bool {
	if client == nil {
		return false
	}
	if reporter, ok := client.(mcpConnectedReporter); ok {
		return reporter.IsConnected()
	}
	return false
}

func (m *MCPClientPool) HasConnection(serverName string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isConnected(m.pool[serverName])
}

func (m *MCPClientPool) Acquire(config *MCPServerConfig) (MCPClient, error) {
	serverName := config.Name

	for {
		m.mu.Lock()
		client := m.pool[serverName]
		_, transitioning := m.transitions[serverName]
		if m.isConnected(client) && !transitioning {
			m.refcounts[serverName]++
			m.configs[serverName] = config
			m.mu.Unlock()
			return client, nil
		}

		transition := m.transitions[serverName]
		shouldConnect := transition == nil
		if shouldConnect {
			transition = newMCPPoolTransition()
			m.transitions[serverName] = transition
			m.configs[serverName] = config
		}
		m.mu.Unlock()

		if !shouldConnect {
			if !transition.wait(mcpPoolTransitionTimeout) {
				m.mu.Lock()
				if m.transitions[serverName] == transition {
					delete(m.transitions, serverName)
					transition.set()
				}
				m.mu.Unlock()
			}
			continue
		}

		newClient := m.clientFactory()(config)
		if err := newClient.Connect(); err != nil {
			m.mu.Lock()
			if m.transitions[serverName] == transition {
				delete(m.transitions, serverName)
				if _, pooled := m.pool[serverName]; !pooled && m.refcounts[serverName] <= 0 {
					delete(m.configs, serverName)
				}
				transition.set()
			}
			m.mu.Unlock()
			return nil, err
		}

		m.mu.Lock()
		if m.transitions[serverName] == transition {
			m.pool[serverName] = newClient
			m.refcounts[serverName]++
			m.configs[serverName] = config
			delete(m.transitions, serverName)
			transition.set()
			m.mu.Unlock()
			return newClient, nil
		}
		m.mu.Unlock()

		_ = newClient.Disconnect()
	}
}

func (m *MCPClientPool) Release(serverName string) error {
	for {
		m.mu.Lock()
		var disconnectClient MCPClient
		shouldDisconnect := false
		transition := m.transitions[serverName]

		if transition == nil {
			refcount := m.refcounts[serverName]
			if refcount <= 0 {
				m.mu.Unlock()
				return nil
			}
			if refcount > 1 {
				m.refcounts[serverName] = refcount - 1
				m.mu.Unlock()
				return nil
			}

			disconnectClient = m.pool[serverName]
			delete(m.pool, serverName)
			delete(m.refcounts, serverName)
			delete(m.configs, serverName)
			transition = newMCPPoolTransition()
			m.transitions[serverName] = transition
			shouldDisconnect = true
		}
		m.mu.Unlock()

		if !shouldDisconnect {
			if !transition.wait(mcpPoolTransitionTimeout) {
				m.mu.Lock()
				if m.transitions[serverName] == transition {
					delete(m.transitions, serverName)
					transition.set()
				}
				m.mu.Unlock()
			}
			continue
		}

		if disconnectClient != nil {
			_ = disconnectClient.Disconnect()
		}
		m.mu.Lock()
		if m.transitions[serverName] == transition {
			delete(m.transitions, serverName)
			transition.set()
		}
		m.mu.Unlock()
		return nil
	}
}

func (m *MCPClientPool) HealthCheck(serverName string) bool {
	var client MCPClient
	var config *MCPServerConfig

	for {
		m.mu.Lock()
		transition := m.transitions[serverName]
		if transition == nil {
			client = m.pool[serverName]
			config = m.configs[serverName]
			m.mu.Unlock()
			break
		}
		m.mu.Unlock()

		if !transition.wait(mcpPoolTransitionTimeout) {
			return false
		}
	}

	if client == nil || config == nil {
		return false
	}

	switch config.Transport {
	case "stdio", "sse":
		_ = client.ListTools()
		if reporter, ok := client.(mcpListErrorReporter); ok {
			if err := reporter.ListToolsError(); err != nil {
				return false
			}
		}
		if reporter, ok := client.(mcpConnectedReporter); ok {
			return reporter.IsConnected()
		}
		return true
	case "http":
		if config.URL == "" {
			return false
		}
		return m.healthProbe()(config) == nil
	case "unix":
		if config.SocketPath == "" {
			return false
		}
		return m.healthProbe()(config) == nil
	default:
		return false
	}
}

func (m *MCPClientPool) Reconnect(serverName string) (MCPClient, error) {
	var (
		transition *mcpPoolTransition
		oldClient  MCPClient
		config     *MCPServerConfig
	)

	for {
		m.mu.Lock()
		transition = m.transitions[serverName]
		if transition == nil {
			config = m.configs[serverName]
			if config == nil {
				m.mu.Unlock()
				return nil, fmt.Errorf("Unknown MCP server: %s", serverName)
			}
			oldClient = m.pool[serverName]
			transition = newMCPPoolTransition()
			m.transitions[serverName] = transition
			m.mu.Unlock()
			break
		}
		m.mu.Unlock()

		if !transition.wait(mcpPoolTransitionTimeout) {
			m.mu.Lock()
			if m.transitions[serverName] == transition {
				delete(m.transitions, serverName)
				transition.set()
			}
			m.mu.Unlock()
		}
	}

	if oldClient != nil {
		_ = oldClient.Disconnect()
	}

	newClient := m.clientFactory()(config)
	if err := newClient.Connect(); err != nil {
		m.mu.Lock()
		if m.transitions[serverName] == transition {
			delete(m.pool, serverName)
			delete(m.transitions, serverName)
			transition.set()
		}
		m.mu.Unlock()
		return nil, err
	}

	m.mu.Lock()
	if m.transitions[serverName] == transition {
		refcount := m.refcounts[serverName]
		if refcount <= 0 {
			delete(m.transitions, serverName)
			transition.set()
			m.mu.Unlock()
			_ = newClient.Disconnect()
			return nil, fmt.Errorf("MCP server '%s' was fully released during reconnect", serverName)
		}
		m.pool[serverName] = newClient
		m.configs[serverName] = config
		m.refcounts[serverName] = refcount
		delete(m.transitions, serverName)
		transition.set()
		m.mu.Unlock()
		return newClient, nil
	}
	m.mu.Unlock()

	_ = newClient.Disconnect()
	return m.Acquire(config)
}

func (m *MCPClientPool) CleanupAll() {
	var (
		clients        map[string]MCPClient
		cleanupEvents  map[string]*mcpPoolTransition
		pendingWaiters []*mcpPoolTransition
	)

	for {
		m.mu.Lock()
		if len(m.transitions) > 0 {
			pendingWaiters = pendingWaiters[:0]
			for _, transition := range m.transitions {
				pendingWaiters = append(pendingWaiters, transition)
			}
			m.mu.Unlock()

			allResolved := true
			for _, transition := range pendingWaiters {
				if !transition.wait(mcpPoolTransitionTimeout) {
					allResolved = false
				}
			}
			if !allResolved {
				m.mu.Lock()
				for name, transition := range m.transitions {
					if !transition.isSet() {
						delete(m.transitions, name)
						transition.set()
					}
				}
				m.mu.Unlock()
			}
			continue
		}

		cleanupEvents = make(map[string]*mcpPoolTransition, len(m.pool))
		clients = make(map[string]MCPClient, len(m.pool))
		for name, client := range m.pool {
			cleanupEvents[name] = newMCPPoolTransition()
			clients[name] = client
			m.transitions[name] = cleanupEvents[name]
		}
		m.pool = make(map[string]MCPClient)
		m.refcounts = make(map[string]int)
		m.configs = make(map[string]*MCPServerConfig)
		m.mu.Unlock()
		break
	}

	for _, client := range clients {
		_ = client.Disconnect()
	}

	m.mu.Lock()
	for name, transition := range cleanupEvents {
		if m.transitions[name] == transition {
			delete(m.transitions, name)
			transition.set()
		}
	}
	m.mu.Unlock()
}

func DefaultMCPHealthProbe(config *MCPServerConfig) error {
	base := config.URL
	if base == "" {
		base = "http://localhost"
	}
	target := strings.TrimRight(base, "/") + "/health"

	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	for name, value := range config.Headers {
		request.Header.Set(name, value)
	}

	client := &http.Client{Timeout: mcpHealthProbeTimeout}
	if config.Transport == "unix" {
		socketPath := config.SocketPath
		client.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socketPath)
			},
		}
	}

	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("MCP health endpoint returned %s", response.Status)
	}
	return nil
}
