package cloudflare

// Settings face: the ordered /settings/<name> probes behind the speed,
// cache, HTTP and network actions.

import (
	"context"
	"net/http"
	"net/url"
)

// settingValue renders one /settings/<name> response the way the
// upstream `.get("value")` does, reporting whether the payload was an object.
func settingValue(reply apiReply) (any, bool) {
	if reply.failure != nil {
		return nil, true
	}
	object := asMap(reply.result)
	if object == nil {
		return nil, false
	}
	return object["value"], true
}

// settingProbe names one setting request and its output key.
type settingProbe struct {
	Output  string
	Setting string
}

// getSettings performs an ordered group of setting reads.
func getSettings(
	ctx context.Context,
	rt *Runtime,
	zoneID string,
	probes []settingProbe,
) map[string]any {
	out := make(map[string]any, len(probes))
	for _, probe := range probes {
		reply := doRequest(
			ctx, rt, http.MethodGet,
			"/zones/"+url.PathEscape(zoneID)+"/settings/"+probe.Setting, nil, nil,
		)
		value, _ := settingValue(reply)
		out[probe.Output] = value
	}
	return out
}

// getSpeedSettings mirrors cloudflare_get_speed_settings.
func getSpeedSettings(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := validateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	return getSettings(ctx, rt, zoneID, []settingProbe{
		{Output: "minify", Setting: "minify"},
		{Output: "brotli", Setting: "brotli"},
		{Output: "rocket_loader", Setting: "rocket_loader"},
	}), nil
}

// getCacheSettings mirrors cloudflare_get_cache_settings.
func getCacheSettings(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := validateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	return getSettings(ctx, rt, zoneID, []settingProbe{
		{Output: "browser_cache_ttl", Setting: "browser_cache_ttl"},
		{Output: "development_mode", Setting: "development_mode"},
		{Output: "cache_level", Setting: "cache_level"},
	}), nil
}

// getHTTPConfig mirrors cloudflare_get_http_config.
func getHTTPConfig(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := validateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	return getSettings(ctx, rt, zoneID, []settingProbe{
		{Output: "http2", Setting: "http2"},
		{Output: "http3", Setting: "http3"},
		{Output: "0rtt", Setting: "0rtt"},
	}), nil
}

// getNetworkSettings mirrors cloudflare_get_network_settings.
func getNetworkSettings(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := validateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	return getSettings(ctx, rt, zoneID, []settingProbe{
		{Output: "websockets", Setting: "websockets"},
		{Output: "onion_routing", Setting: "onion_routing"},
		{Output: "ipv6", Setting: "ipv6"},
	}), nil
}
