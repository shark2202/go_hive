package cli

// Native HTTP family wiring for the session catalogue.
//
// http_request / http_download / http_upload are Go-only, post-parity tools
// implemented in internal/nativehttp. They are not integration registry tools:
// the catalogue owns one adapter per session, advertises the definitions through
// the lazy tier, and routes calls explicitly. The adapter's cookie jar is
// per-session (WithRoot starts a fresh one for a colony worker), so cookie
// state can never cross Hive chat sessions.
//
// Configuration comes from $HIVE_HOME/http_tools.json only. The session
// directory is the file tools' sandbox root and is agent-writable; it is never
// a config source. See go_hive/docs/config-roots.html.

import (
	"context"

	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// registerHTTPTools appends the native HTTP definitions when
// <HIVE_HOME>/http_tools.json enables the family (default enabled).
func (c *sessionToolCatalogue) registerHTTPTools() {
	if c == nil || !builtin.HTTPEnabled(c.home.Root) {
		return
	}
	c.httpToolbox = builtin.NewHTTPToolbox(c.root, c.home.Root)
	for _, def := range c.httpToolbox.Defs() {
		if _, taken := c.builtinNames[def.Name]; taken {
			continue
		}
		c.defs = append(c.defs, def)
	}
}

// executeHTTPTool routes one http_* call through the session adapter. handled
// is false for every other name so the normal catalogue dispatch still runs.
func (c *sessionToolCatalogue) executeHTTPTool(ctx context.Context, name string, input map[string]any) (string, bool, bool, error) {
	if c == nil || c.httpToolbox == nil {
		return "", false, false, nil
	}
	switch name {
	case builtin.ToolHTTPRequest, builtin.ToolHTTPDownload, builtin.ToolHTTPUpload:
		content, isError, err := c.httpToolbox.Execute(ctx, name, input)
		return content, isError, true, err
	default:
		return "", false, false, nil
	}
}
