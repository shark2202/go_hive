package cloudflare

// Catalog assembly: the per-face row slices are the single source of truth
// for both the model-facing schema and the dispatcher. actionCatalog
// concatenates them in upstream action order, because that order fixes the
// tool's action enum.

import "context"

// actionSpec is one upstream @mcp.tool function. The default runner
// sends a single request built from Path/Query/BodyMap/Consts and wraps the
// decoded result. Shape post-processes that result; Run replaces the whole
// single-request flow for multi-call or conditional actions.
type actionSpec struct {
	Name     string
	Summary  string
	Params   map[string]any
	Required []string
	Method   string
	Path     string
	Query    []string
	BodyMap  map[string]string
	Consts   map[string]any
	// Wrap is the response key the result is published under. Actions without
	// an upstream `if "error" in result: return result` guard set WrapErrors so
	// the provider failure lands under that key untouched.
	Wrap       string
	WrapErrors bool
	Shape      func(apiReply, map[string]any) any
	Run        func(context.Context, *Runtime, map[string]any) (any, error)
}

// actionCatalog returns every action row in upstream order.
func actionCatalog() []actionSpec {
	rows := make([]actionSpec, 0, 54)
	rows = append(rows, zoneRows...)
	rows = append(rows, dnsRows...)
	rows = append(rows, analyticsRows...)
	rows = append(rows, securityRows...)
	rows = append(rows, performanceRows...)
	rows = append(rows, accountRows...)
	rows = append(rows, serviceRows...)
	return rows
}

// newTool builds the model-facing tool from the action table.
func newTool() *Tool {
	rows := actionCatalog()
	actions := make([]Action, 0, len(rows))
	for _, row := range rows {
		actions = append(actions, Action{
			Name:     row.Name,
			Summary:  row.Summary,
			Source:   "cloudflare_tool/cloudflare_tool.py",
			Params:   row.Params,
			Required: row.Required,
		})
	}
	return &Tool{
		ID:          moduleID,
		Name:        "cloudflare",
		DisplayName: "Cloudflare",
		Summary:     "Cloudflare zone, DNS, analytics, security, cache, Workers and account management (54 actions).",
		Actions:     actions,
	}
}
