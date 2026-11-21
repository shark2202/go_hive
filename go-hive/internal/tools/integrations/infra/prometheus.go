package infra

// Prometheus: catalog, the instant/range query actions and the upstream
// transport-error split.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

var prometheusTool = &Tool{
	ID:          "prometheus_tool",
	Name:        "prometheus",
	DisplayName: "Prometheus",
	Summary:     "Prometheus PromQL instant and range queries (2 actions).",
	Actions: []Action{
		{
			Name:     "prometheus_query",
			Summary:  "Query Prometheus using PromQL.",
			Source:   "prometheus_tool/prometheus_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":   param("string"),
				"timeout": paramDefault("integer", 5),
			},
		},
		{
			Name:     "prometheus_query_range",
			Summary:  "Query Prometheus over a time range using PromQL.",
			Source:   "prometheus_tool/prometheus_tool.py",
			Required: []string{"query", "start", "end"},
			Params: map[string]any{
				"query":   param("string"),
				"start":   param("string"),
				"end":     param("string"),
				"step":    paramDefault("string", "60s"),
				"timeout": paramDefault("integer", 5),
			},
		},
	},
}

func prometheusImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"prometheus_query":       prometheusQuery,
		"prometheus_query_range": prometheusQueryRange,
	}
}

// prometheusAuth mirrors upstream _get_auth: Bearer token wins,
// then optional basic auth, otherwise unauthenticated.
func prometheusAuth(rt *Runtime) map[string]string {
	if token := strings.TrimSpace(runtimeEnv(rt, "PROMETHEUS_TOKEN")); token != "" {
		return map[string]string{"Authorization": "Bearer " + token}
	}
	username := runtimeEnv(rt, "PROMETHEUS_USERNAME")
	password := runtimeEnv(rt, "PROMETHEUS_PASSWORD")
	if username != "" && password != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		return map[string]string{"Authorization": "Basic " + encoded}
	}
	return nil
}

// prometheusBase mirrors upstream _get_prometheus_base_url:
// credential store (WithBaseURL) wins over PROMETHEUS_BASE_URL.
func prometheusBase(rt *Runtime) string {
	return strings.TrimRight(endpoint(rt, "prometheus_tool", "PROMETHEUS_BASE_URL", ""), "/")
}

// prometheusTimeout clamps to the upstream 1..30 second window.
func prometheusTimeout(input map[string]any) int {
	timeout := intArg(input, "timeout", defaultPrometheusSec)
	if timeout < 1 || timeout > 30 {
		return defaultPrometheusSec
	}
	return timeout
}

// prometheusTransport maps transport failures the way upstream
// distinguishes timeout, connection failure and everything else.
func prometheusTransport(ctx context.Context, err error) map[string]any {
	if isTimeout(ctx, err) {
		return map[string]any{"error": "Request to Prometheus timed out"}
	}
	var netErr *net.OpError
	var dnsErr *net.DNSError
	if errors.As(err, &netErr) || errors.As(err, &dnsErr) {
		return map[string]any{
			"error": "Failed to connect to Prometheus",
			"help":  "Check if Prometheus is running and base_url is correct",
		}
	}
	return map[string]any{"error": "Unexpected error: " + err.Error()}
}

// prometheusMissing renders the upstream missing-credential dict.
func prometheusMissing() map[string]any {
	return map[string]any{
		"error":   "Missing required credential: Base URL of Prometheus server",
		"help":    prometheusHelp,
		"success": false,
	}
}

// prometheusRun performs one query and shapes the response. label
// is the upstream failure noun ("Prometheus query" / "Prometheus range query").
// The boolean reports whether the query succeeded.
func prometheusRun(ctx context.Context, rt *Runtime, endpoint string, params url.Values, timeoutSeconds int, label string) (map[string]any, bool) {
	base := prometheusBase(rt)
	if base == "" {
		return prometheusMissing(), false
	}
	resp, err := send(ctx, rt, http.MethodGet, addPrefix(base, endpoint)+"?"+params.Encode(),
		prometheusAuth(rt), nil, time.Duration(timeoutSeconds)*time.Second)
	if err != nil {
		return prometheusTransport(ctx, err), false
	}
	if resp.Status != http.StatusOK {
		return map[string]any{
			"error":   fmt.Sprintf("Prometheus returned status %d", resp.Status),
			"details": string(resp.Body),
		}, false
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Unexpected error: invalid JSON response"}, false
	}
	if asString(data["status"]) != "success" {
		return map[string]any{"error": label + " failed", "details": data}, false
	}
	return map[string]any{
		"success": true,
		"result":  listOrEmpty(asMap(data["data"])["result"]),
		"raw":     data,
	}, true
}

func prometheusQuery(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if query == "" || utf8.RuneCountInString(query) > 1000 {
		return map[string]any{"error": "Query must be between 1 and 1000 characters"}, nil
	}
	timeout := prometheusTimeout(input)
	result, ok := prometheusRun(ctx, rt, "/api/v1/query",
		url.Values{"query": []string{query}}, timeout, "Prometheus query")
	if ok {
		result["query"] = query
	}
	return result, nil
}

func prometheusQueryRange(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if query == "" || utf8.RuneCountInString(query) > 1000 {
		return map[string]any{"error": "Query must be between 1 and 1000 characters"}, nil
	}
	start := stringArg(input, "start")
	end := stringArg(input, "end")
	if start == "" || end == "" {
		return map[string]any{"error": "start and end time are required"}, nil
	}
	step := stringArg(input, "step")
	timeout := prometheusTimeout(input)
	params := url.Values{
		"query": []string{query},
		"start": []string{start},
		"end":   []string{end},
		"step":  []string{step},
	}
	result, ok := prometheusRun(ctx, rt, "/api/v1/query_range", params, timeout, "Prometheus range query")
	if ok {
		result["query"] = query
		result["start"] = start
		result["end"] = end
		result["step"] = step
	}
	return result, nil
}
