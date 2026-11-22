package crm

// Shared JSON accessors and small runtime helpers. They existed once in the
// root package's kit_shared.go as forwards to internal/tools/integrationkit;
// the moved provider code keeps its terse unqualified names through the same
// forwards here.

import (
	"context"
	"time"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

func isTimeout(_ context.Context, err error) bool { return integrationkit.IsTimeout(err) }

func asMap(value any) map[string]any { return integrationkit.AsMap(value) }

func asSlice(value any) []any { return integrationkit.AsSlice(value) }

func asString(value any) string { return integrationkit.AsString(value) }

func asBool(value any) bool { return integrationkit.AsBool(value) }

func boolOr(value any, fallback bool) bool { return integrationkit.BoolOr(value, fallback) }

func sliceOrEmpty(value any) []any { return integrationkit.SliceOrEmpty(value) }

func mapOrEmpty(value any) map[string]any { return integrationkit.MapOrEmpty(value) }

func jsonNumber(value any) any { return integrationkit.JSONNumber(value) }

func decodeObject(raw []byte) (map[string]any, bool) { return integrationkit.DecodeObject(raw) }

func jsonObject(raw []byte) (map[string]any, bool) { return integrationkit.JSONObject(raw) }

func decodeAny(raw []byte) (any, bool) { return integrationkit.DecodeAny(raw) }

func int64Value(value any) (int64, bool) { return integrationkit.Int64(value) }

func optionalInt(input map[string]any, name string) (int, bool) {
	return integrationkit.OptionalInt(input, name)
}

func optionalString(input map[string]any, name string) (string, bool) {
	return integrationkit.OptionalString(input, name)
}

func stringID(value any) string { return integrationkit.StringID(value) }

func stringOr(object map[string]any, name, fallback string) string {
	return integrationkit.StringOr(object, name, fallback)
}

func valueOr(object map[string]any, key string, fallback any) any {
	return integrationkit.ValueOr(object, key, fallback)
}

func clampInt(value, low, high int) int { return integrationkit.ClampInt(value, low, high) }

func clampFloat(value, low, high float64) float64 {
	return integrationkit.ClampFloat(value, low, high)
}

func truncateText(text string, limit int) string {
	return integrationkit.TruncateText(text, limit)
}

func truncateTextClamped(text string, limit int) string {
	return integrationkit.TruncateTextClamped(text, limit)
}

func splitCSV(raw string) []string { return integrationkit.SplitCSV(raw) }

func pathEscape(value string) string { return integrationkit.PathEscape(value) }

func basicAuth(user, password string) string {
	return integrationkit.BasicAuth(user, password)
}

func minInt(left, right int) int { return integrationkit.MinInt(left, right) }

func failureResult(result map[string]any) (map[string]any, bool) {
	return integrationkit.Failure(result)
}

func bearerJSONHeaders(token string) map[string]string {
	return integrationkit.BearerJSONHeaders(token)
}

func numberValue(value any) (float64, bool) { return integrationkit.Number(value) }

// moduleBaseURL resolves a module base URL, honouring WithBaseURL.
func moduleBaseURL(rt *Runtime, moduleID string) string {
	return rt.ModuleBaseURL(moduleID)
}

func runtimeHTTP(rt *Runtime) *httpClient { return rt.HTTPClientOrNew() }

func runtimeGetenv(rt *Runtime) func(string) string { return rt.GetenvFunc() }

func runtimeNow(rt *Runtime) time.Time { return rt.NowOrWall() }

func runtimeEnv(rt *Runtime, name string) string { return rt.EnvOrOS(name) }

func runtimeEnvTrimmed(rt *Runtime, name string) string { return rt.EnvTrimmed(name) }
