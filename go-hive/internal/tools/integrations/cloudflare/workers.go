package cloudflare

// Workers/advanced-services face: Workers scripts plus load balancers, and
// the SSL-for-SaaS (custom hostname) / Zero Trust access list shapes.

import (
	"context"
	"net/http"
	"net/url"
)

// shapeCustomHostnames mirrors cloudflare_list_custom_hostnames.
func shapeCustomHostnames(reply apiReply, _ map[string]any) any {
	if items := sliceValue(reply.result); items != nil {
		return map[string]any{"custom_hostnames": items}
	}
	return map[string]any{
		"custom_hostnames": sliceAt(asMap(reply.result), "custom_hostnames"),
	}
}

// shapeAccessApplications mirrors cloudflare_list_access_applications.
func shapeAccessApplications(reply apiReply, _ map[string]any) any {
	items := sliceValue(reply.result)
	if items == nil {
		items = []any{}
	}
	return map[string]any{"access_applications": items}
}

// listAdvancedServices mirrors cloudflare_list_advanced_services.
func listAdvancedServices(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	zoneID := stringArg(input, "zone_id")
	if failure := validateZoneID(zoneID); failure != nil {
		return failure, nil
	}
	workers := doRequest(
		ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/workers/scripts", nil, nil,
	)
	loadBalancers := doRequest(
		ctx, rt, http.MethodGet, "/zones/"+url.PathEscape(zoneID)+"/load_balancers", nil, nil,
	)
	workerCount := len(sliceValue(workers.value()))
	balancers := sliceValue(loadBalancers.value())
	if balancers == nil {
		balancers = []any{}
	}
	return map[string]any{
		"workers_count":        workerCount,
		"load_balancers_count": len(balancers),
		"load_balancers":       balancers,
	}, nil
}
