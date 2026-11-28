package scrape

// Dependency seams for the web-scrape action: the DNS resolver behind the SSRF
// gate, the browser renderer and the home-directory resolver for artifact
// paths. Tests inject every seam so no assertion launches a browser or depends
// on $HOME.

import (
	"context"
	"net"
)

// Resolver resolves a hostname to its addresses for the SSRF gate.
// Runtime.ResolveHost has the same shape, so a fake resolver keeps the SSRF
// tests off the network.
type Resolver func(context.Context, string) ([]string, error)

// Renderer returns the post-JavaScript DOM for a URL. Production runs the
// shared headless browser (browserRenderer); tests inject a fake so extraction
// fixtures never need a browser. staticHTML is the pre-flight body — production
// ignores it, tests may return it to stand in for page.content() on a static
// fixture.
type Renderer func(ctx context.Context, target, staticHTML string) (string, error)

// Deps carries the injectable side effects of the web_scrape tool. Register
// fills nil fields with the process defaults, so the zero value is usable.
type Deps struct {
	Resolver    Resolver
	Renderer    Renderer
	UserHomeDir func() (string, error)
}

// Option mutates the scrape dependency bundle.
type Option func(*Deps)

// WithResolver swaps the DNS resolver behind the SSRF gate.
func WithResolver(resolver Resolver) Option {
	return func(deps *Deps) { deps.Resolver = resolver }
}

// WithRenderer replaces the browser renderer (tests).
func WithRenderer(renderer Renderer) Option {
	return func(deps *Deps) { deps.Renderer = renderer }
}

// WithHomeDir overrides the home directory used by "~" expansion in the scrape
// artifact path (tests never read the developer's $HOME).
func WithHomeDir(fn func() (string, error)) Option {
	return func(deps *Deps) { deps.UserHomeDir = fn }
}

// DefaultDeps returns the production seams: the system resolver and the shared
// headless browser.
func DefaultDeps() Deps {
	return Deps{
		Resolver: net.DefaultResolver.LookupHost,
		Renderer: browserRenderer,
	}
}

// withDefaults fills the seams a caller left nil.
func (deps Deps) withDefaults() Deps {
	if deps.Resolver == nil {
		deps.Resolver = net.DefaultResolver.LookupHost
	}
	if deps.Renderer == nil {
		deps.Renderer = browserRenderer
	}
	return deps
}
