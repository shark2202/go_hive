package browserbridge

import (
	"context"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// health.go ports tools/src/gcu/browser/health.py: pure rules over the raw
// tab.audit snapshot, plus the bridge's blocker cache and /tabs/{id}/health
// probe. The extension owns observation; this file owns interpretation.

var chromeExtensionURLRe = regexp.MustCompile(`^chrome-extension://([a-z0-9]+)/`)

// Known foreign-extension offenders, copied from upstream health.py.
var knownExtensions = map[string]string{
	"cbhilkcodigmigfbnphipnnmamjfkipp": "Calendly",
	"kbfnbcaeplbcioakkpcpgfkobkghlhen": "Grammarly",
	"hdokiejnpimakedhajhdlcegeplioahd": "LastPass",
	"aeblfdkhhhdcdjpifhhbdiojplfjncoa": "1Password",
	"cjpalhdlnbpafiamejdnhcphjbkeiagm": "uBlock Origin",
	"gighmmpiobklfepjocnamgkkbiglidom": "AdBlock",
	"cfhdojbkjhnklbpkdaibdccddilifddb": "Adblock Plus",
	"bmnlcjabgnpnenekpadlanbbkooimhnj": "Honey",
	"mlomiejdfkolichcflejclcbmpeaniij": "Ghostery",
	"nmmhkkegccagdldgiimedpiccmgmieda": "Chrome PDF Viewer",
}

func extensionIDFromURL(raw string) string {
	m := chromeExtensionURLRe.FindStringSubmatch(raw)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

func lookupExtension(id string) (string, bool) {
	name, ok := knownExtensions[id]
	if ok {
		return name, true
	}
	return "an unknown extension", false
}

func chromeWebStoreURL(id string) string {
	return "https://chromewebstore.google.com/detail/" + id
}

type frameURL struct {
	url        string
	declaredID string
}

func iterFrameURLs(snapshot map[string]any) []frameURL {
	var out []frameURL
	appendRows := func(key string) {
		rows, _ := snapshot[key].([]any)
		for _, row := range rows {
			m, ok := row.(map[string]any)
			if !ok {
				continue
			}
			u, _ := m["url"].(string)
			if u == "" {
				u, _ = m["src"].(string)
			}
			id, _ := m["extensionId"].(string)
			out = append(out, frameURL{url: u, declaredID: id})
		}
	}
	appendRows("domFrames")
	appendRows("targets")
	appendRows("cdpFrames")
	return out
}

var privilegedPrefixes = []string{
	"chrome://",
	"chrome-untrusted://",
	"devtools://",
	"view-source:",
	"chrome-search://",
	"chrome-error://",
}

func rulePrivilegedScheme(snap map[string]any) *Blocker {
	raw, _ := snap["url"].(string)
	if strings.HasPrefix(raw, "chrome://newtab") ||
		strings.HasPrefix(raw, "chrome://new-tab-page") ||
		strings.HasPrefix(raw, "chrome-search://local-ntp") {
		return nil
	}
	for _, prefix := range privilegedPrefixes {
		if !strings.HasPrefix(raw, prefix) {
			continue
		}
		scheme := strings.TrimRight(prefix, ":/")
		return &Blocker{
			Kind: "privileged_scheme", Severity: "block",
			Title:    "Privileged Chrome page",
			Detail:   "This tab is on " + scheme + ", which Chrome forbids extensions from automating.",
			Fix:      "Navigate to a regular http(s) page or open a new tab.",
			Priority: 10,
			Context:  map[string]any{"url": raw, "scheme": scheme},
		}
	}
	return nil
}

func ruleChromeWebStore(snap map[string]any) *Blocker {
	raw, _ := snap["url"].(string)
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if host == "chromewebstore.google.com" || (host == "chrome.google.com" && strings.HasPrefix(u.Path, "/webstore")) {
		return &Blocker{
			Kind: "chrome_web_store", Severity: "block",
			Title:    "Chrome Web Store",
			Detail:   "Chrome hard-codes a refusal to let any extension automate the Web Store.",
			Fix:      "Navigate to a different site.",
			Priority: 15,
			Context:  map[string]any{"url": raw},
		}
	}
	return nil
}

func foreignExtensionBlocker(ids []string, priority int) *Blocker {
	first := ids[0]
	name, known := lookupExtension(first)
	subject := name
	if subject != "" {
		subject = strings.ToUpper(subject[:1]) + subject[1:]
	}
	extra := ""
	if len(ids) > 1 {
		extra = " (+" + itoa(len(ids)-1) + " more)"
	}
	return &Blocker{
		Kind: "foreign_extension_frame", Severity: "block",
		Title:    "Blocked by " + name,
		Detail:   subject + " (" + chromeWebStoreURL(first) + ") injected a frame into this tab" + extra + ", which prevents Hive from automating the page.",
		Fix:      "Open chrome://extensions/?id=" + first + ", turn it off, and reload this tab.",
		Priority: priority,
		Context: map[string]any{
			"offender_extension_id": first,
			"offender_name":         name,
			"offender_known":        known,
			"agent_action":          "Surface the blocker, then ask before opening chrome://extensions — call browser_open with the fix URL only if the user agrees.",
		},
	}
}

func ruleForeignExtensionFrame(snap map[string]any, ctx map[string]any) *Blocker {
	own, _ := snap["ourExtensionId"].(string)
	if own == "" {
		own, _ = ctx["our_extension_id"].(string)
	}
	if own == "" {
		return nil
	}
	seen := map[string]struct{}{}
	for _, f := range iterFrameURLs(snap) {
		id := f.declaredID
		if id == "" {
			id = extensionIDFromURL(f.url)
		}
		if id != "" && id != own {
			seen[id] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return foreignExtensionBlocker(ids, 20)
}

func ruleForeignExtensionFromError(snap map[string]any, errMsg string, ctx map[string]any) *Blocker {
	if errMsg == "" {
		return nil
	}
	lower := strings.ToLower(errMsg)
	if !strings.Contains(lower, "chrome-extension://") || !strings.Contains(lower, "different extension") {
		return nil
	}
	own, _ := snap["ourExtensionId"].(string)
	if own == "" {
		own, _ = ctx["our_extension_id"].(string)
	}
	var ids []string
	seen := map[string]struct{}{}
	if own != "" {
		for _, f := range iterFrameURLs(snap) {
			id := f.declaredID
			if id == "" {
				id = extensionIDFromURL(f.url)
			}
			if id == "" || id == own {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) > 0 {
		sort.Strings(ids)
		return foreignExtensionBlocker(ids, 21)
	}
	return &Blocker{
		Kind: "foreign_extension_frame", Severity: "block",
		Title:    "Blocked by another extension",
		Detail:   "Chrome refused a debugger command because another extension's iframe is in this tab, but Hive hasn't been able to identify which one yet. The extension may have re-injected after attach.",
		Fix:      "Open chrome://extensions and disable extensions that overlay content on this site (common culprits: Calendly, Grammarly, ad-blockers, password managers), then retry.",
		Priority: 21,
		Context:  map[string]any{"matched_from": "error_no_snapshot"},
	}
}

func ruleDevToolsAttached(errMsg string) *Blocker {
	if errMsg == "" {
		return nil
	}
	lower := strings.ToLower(errMsg)
	hit := strings.Contains(lower, "another debugger") ||
		strings.Contains(lower, "debugger is already attached") ||
		(strings.Contains(lower, "cannot attach") && strings.Contains(lower, "devtools"))
	if !hit {
		return nil
	}
	return &Blocker{
		Kind: "devtools_attached", Severity: "block",
		Title:    "DevTools is open on this tab",
		Detail:   "Only one debugger client per tab is allowed; a human DevTools window is holding it.",
		Fix:      "Close DevTools on the target tab.",
		Priority: 30,
		Context:  map[string]any{},
	}
}

func ruleEnterprisePolicy(errMsg string) *Blocker {
	if errMsg == "" {
		return nil
	}
	lower := strings.ToLower(errMsg)
	if !strings.Contains(lower, "policy") || (!strings.Contains(lower, "debug") && !strings.Contains(lower, "remote")) {
		return nil
	}
	return &Blocker{
		Kind: "enterprise_policy", Severity: "block",
		Title:    "Blocked by Chrome policy",
		Detail:   "A Chrome enterprise policy disabled the debugger API on this device.",
		Fix:      "Contact your IT admin — RemoteDebuggingAllowed must be enabled.",
		Priority: 40,
		Context:  map[string]any{},
	}
}

func ruleFileAccessDenied(snap map[string]any) *Blocker {
	raw, _ := snap["url"].(string)
	if !strings.HasPrefix(raw, "file://") {
		return nil
	}
	if allowed, ok := snap["fileAccess"].(bool); ok && allowed {
		return nil
	}
	return &Blocker{
		Kind: "file_access_denied", Severity: "block",
		Title:    "File access not allowed",
		Detail:   "The Hive extension isn't permitted to operate on file:// URLs.",
		Fix:      `Open chrome://extensions, find Hive Browser Bridge → Details, and enable "Allow access to file URLs".`,
		Priority: 50,
		Context:  map[string]any{},
	}
}

// classifyHealth returns every matching blocker, deduped by kind and ordered
// by ascending priority (first match wins for single-blocker consumers).
func classifyHealth(snapshot map[string]any, errMsg string, ctx map[string]any) []Blocker {
	if snapshot == nil {
		snapshot = map[string]any{}
	}
	if ctx == nil {
		ctx = map[string]any{}
	}
	candidates := []*Blocker{
		rulePrivilegedScheme(snapshot),
		ruleChromeWebStore(snapshot),
		ruleForeignExtensionFrame(snapshot, ctx),
		ruleForeignExtensionFromError(snapshot, errMsg, ctx),
		ruleDevToolsAttached(errMsg),
		ruleEnterprisePolicy(errMsg),
		ruleFileAccessDenied(snapshot),
	}
	seen := map[string]struct{}{}
	out := make([]Blocker, 0, len(candidates))
	for _, b := range candidates {
		if b == nil {
			continue
		}
		if _, dup := seen[b.Kind]; dup {
			continue
		}
		seen[b.Kind] = struct{}{}
		if b.Context == nil {
			b.Context = map[string]any{}
		}
		out = append(out, *b)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

// refreshBlockers mirrors upstream _refresh_blockers: a fresh snapshot is
// cached, classification runs over snapshot + optional error, and a clean
// proactive audit clears stale blockers (an error path does not).
func (b *Bridge) refreshBlockers(tabID int, snapshot map[string]any, errMsg string, clearIfClean bool) []Blocker {
	if snapshot != nil {
		b.setTabSnapshot(tabID, snapshot)
	}
	snap := snapshot
	if snap == nil {
		snap = b.tabSnapshot(tabID)
	}
	blockers := classifyHealth(snap, errMsg, nil)
	if len(blockers) > 0 {
		b.setTabBlockers(tabID, blockers)
	} else if errMsg == "" && clearIfClean {
		b.setTabBlockers(tabID, nil)
	}
	return blockers
}

func (b *Bridge) refreshBlockersFromError(tabID int, errMsg string) {
	if tabID <= 0 || errMsg == "" {
		return
	}
	b.refreshBlockers(tabID, nil, errMsg, false)
}

// auditTab fetches a raw tab.audit snapshot for tabID.
func (b *Bridge) auditTab(ctx context.Context, tabID int) (map[string]any, error) {
	conn, err := b.resolve("", &tabID)
	if err != nil {
		return nil, err
	}
	raw, err := conn.Call(ctx, "tab.audit", map[string]any{"tabId": tabID}, 15*time.Second)
	if err != nil {
		return nil, err
	}
	return resultMap(raw)
}

// AuditTab is the public raw observation probe (upstream audit_tab).
func (b *Bridge) AuditTab(ctx context.Context, tabID int) (map[string]any, error) {
	b.touchTab(tabID)
	snap, err := b.auditTab(ctx, tabID)
	if err != nil {
		return nil, err
	}
	b.setTabSnapshot(tabID, snap)
	return snap, nil
}

// TabHealth returns the upstream /tabs/{id}/health payload. It re-classifies
// only when a fresh snapshot was fetched; a fresh empty result never clears
// blockers populated by a CDP-error path.
func (b *Bridge) TabHealth(ctx context.Context, tabID int, forceAudit bool) map[string]any {
	b.touchTab(tabID)
	var fresh map[string]any
	if forceAudit || !b.hasTabSnapshot(tabID) {
		if snap, err := b.auditTab(ctx, tabID); err == nil {
			fresh = snap
		} else {
			b.cfg.Logf("tab.audit failed for tab %d: %v", tabID, err)
		}
	}
	if fresh != nil {
		b.setTabSnapshot(tabID, fresh)
		blockers := classifyHealth(fresh, "", nil)
		if len(blockers) > 0 {
			b.setTabBlockers(tabID, blockers)
		}
	}
	blockers := b.tabBlockersFor(tabID)
	if blockers == nil {
		blockers = []Blocker{}
	}
	// Optional external provider augmentation, deduped by kind.
	seen := make(map[string]struct{}, len(blockers))
	for _, blk := range blockers {
		seen[blk.Kind] = struct{}{}
	}
	for _, blk := range b.health.Blockers(ctx, tabID) {
		if blk.Kind == "" {
			continue
		}
		if _, dup := seen[blk.Kind]; dup {
			continue
		}
		seen[blk.Kind] = struct{}{}
		blockers = append(blockers, blk)
	}
	snap := fresh
	if snap == nil {
		snap = b.tabSnapshot(tabID)
	}
	u, _ := snap["url"].(string)
	return map[string]any{
		"ok":       true,
		"tab_id":   tabID,
		"url":      u,
		"blockers": blockers,
	}
}

// GetTabBlockers returns the cached blocker list (may be empty).
func (b *Bridge) GetTabBlockers(tabID int) []Blocker {
	out := b.tabBlockersFor(tabID)
	if out == nil {
		return []Blocker{}
	}
	return out
}

// SystemBlocker is the connection-level rollup: a blocker kind is promoted
// only when EVERY active context has at least one severity=block blocker of
// that kind. Returns a copy, or nil.
func (b *Bridge) SystemBlocker() *Blocker {
	metas := b.contexts.List()
	perProfile := map[string]map[string]struct{}{}
	for _, meta := range metas {
		if meta.GroupID > 0 {
			perProfile[meta.Profile] = map[string]struct{}{}
		}
	}
	if len(perProfile) == 0 {
		return nil
	}
	samples := map[string]Blocker{}
	for tab, blockers := range b.blockersSnapshot() {
		profile := b.tabOwnerOf(tab)
		kinds, ok := perProfile[profile]
		if !ok {
			continue
		}
		for _, blk := range blockers {
			if blk.Severity != "block" || blk.Kind == "" {
				continue
			}
			kinds[blk.Kind] = struct{}{}
			if _, exists := samples[blk.Kind]; !exists {
				samples[blk.Kind] = blk
			}
		}
	}
	var shared map[string]struct{}
	for _, kinds := range perProfile {
		if len(kinds) == 0 {
			return nil
		}
		if shared == nil {
			shared = map[string]struct{}{}
			for kind := range kinds {
				shared[kind] = struct{}{}
			}
			continue
		}
		for kind := range shared {
			if _, ok := kinds[kind]; !ok {
				delete(shared, kind)
			}
		}
	}
	if len(shared) == 0 {
		return nil
	}
	chosen := ""
	best := int(^uint(0) >> 1)
	for kind := range shared {
		p := samples[kind].Priority
		if p < best {
			best = p
			chosen = kind
		}
	}
	if chosen == "" {
		return nil
	}
	out := samples[chosen]
	return &out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
