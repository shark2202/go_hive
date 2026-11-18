package runtimehealth

import (
	"strings"
	"testing"
)

// Parity-exempt: framework/host/runtime_health.py has no Python test; these

func TestNetworkErrorMarkersClassifyConnectivityFailures(t *testing.T) {
	for _, text := range []string{
		"Temporary failure in name resolution",
		"[Errno -2] Name or service not known",
		"Cannot connect to host api.example.com",
		"Connection refused",
		"Connection reset by peer",
		"Network is unreachable",
		"No route to host",
		"SSL handshake timed out",
		"SSL: HANDSHAKE_FAILURE",
		"Server remote disconnected",
		"Connect timeout",
		"aiohttp.client_exceptions.ClientConnectorError: ConnectionError",
	} {
		if !IsUpstreamNetworkError(text) {
			t.Fatalf("%q should read as a network error", text)
		}
	}
	for _, text := range []string{
		"401 Unauthorized",
		"invalid schema for tool",
		"payment required",
		"",
	} {
		if IsUpstreamNetworkError(text) {
			t.Fatalf("%q should not read as a network error", text)
		}
	}
}

// Parity-exempt: framework/host/runtime_health.py has no Python test; this
func TestUpstreamHealthLifecycle(t *testing.T) {
	Reset()
	if Current().Degraded {
		t.Fatal("fresh state should be healthy")
	}

	MarkUpstreamDegraded("first failure")
	first := Current()
	if !first.Degraded || first.Reason == nil || *first.Reason != "first failure" {
		t.Fatalf("after degrade = %#v", first)
	}
	if first.SinceEpoch == nil || first.LastEventEpoch == nil {
		t.Fatalf("degrade should stamp both epochs: %#v", first)
	}
	since := *first.SinceEpoch

	MarkUpstreamDegraded("second failure")
	second := Current()
	if second.Reason == nil || *second.Reason != "second failure" {
		t.Fatalf("reason should refresh: %#v", second)
	}
	if second.SinceEpoch == nil || *second.SinceEpoch != since {
		t.Fatalf("since_epoch should keep the original onset: %#v", second)
	}

	MarkUpstreamHealthy()
	healthy := Current()
	if healthy.Degraded || healthy.Reason != nil || healthy.SinceEpoch != nil {
		t.Fatalf("healthy state should clear the flag: %#v", healthy)
	}
	if healthy.LastEventEpoch == nil {
		t.Fatal("healthy transition should stamp last_event_epoch")
	}
}

// Parity-exempt: framework/host/runtime_health.py has no Python test; this
func TestDegradedReasonIsTruncatedTo240Characters(t *testing.T) {
	Reset()
	MarkUpstreamDegraded(strings.Repeat("x", 300))
	reason := Current().Reason
	if reason == nil || len([]rune(*reason)) != 240 {
		t.Fatalf("reason length = %d, want 240", len([]rune(*reason)))
	}
}
