package browserbridge

// commands_page_snapshot.go holds the snapshot helpers shared by
// cmdSnapshot: the href collector, the DOM fallback and the InlineTextBox
// de-duplication — ports of bridge.py `_collect_link_hrefs`,
// `_dom_snapshot` and `_clean_inline_text_boxes`.

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// linkHrefsJS walks every visible <a href> and returns [name, href] pairs in
// document order. The name walk approximates the W3C AccName algorithm: it
// skips aria-hidden subtrees so names match the AX tree's own computation.
const linkHrefsJS = `(function() {
  var nameOf = function(a) {
    var aria = a.getAttribute('aria-label');
    if (aria && aria.trim()) return aria.trim().replace(/\s+/g, ' ');
    var parts = [];
    (function walk(node) {
      if (node.nodeType === 3) { parts.push(node.textContent); return; }
      if (node.nodeType !== 1) return;
      if (node.getAttribute && node.getAttribute('aria-hidden') === 'true') return;
      var kids = node.childNodes;
      for (var i = 0; i < kids.length; i++) walk(kids[i]);
    })(a);
    var txt = parts.join(' ').replace(/\s+/g, ' ').trim();
    if (txt) return txt;
    var title = a.getAttribute('title');
    if (title && title.trim()) return title.trim();
    return '';
  };
  var out = [];
  var anchors = document.querySelectorAll('a[href]');
  for (var i = 0; i < anchors.length; i++) {
    var a = anchors[i];
    var rect = a.getBoundingClientRect();
    if (rect.width === 0 && rect.height === 0) continue;
    var s = window.getComputedStyle(a);
    if (s.display === 'none' || s.visibility === 'hidden') continue;
    out.push([nameOf(a), a.href]);
  }
  return out;
})()`

// collectLinkHrefs returns hrefs grouped by accessible name and the names in
// first-seen order (Go maps are unordered; the formatter needs DOM order to
// pop hrefs deterministically). Any failure yields an empty map — links just
// won't be href-annotated.
func (b *Bridge) collectLinkHrefs(ctx context.Context, conn *Connection, tabID int) (map[string][]string, []string) {
	v, err := b.evalJS(ctx, conn, tabID, linkHrefsJS, 10*time.Second)
	if err != nil {
		return nil, nil
	}
	pairs, ok := v.([]any)
	if !ok {
		return nil, nil
	}
	byName := map[string][]string{}
	var order []string
	for _, entry := range pairs {
		pair, ok := entry.([]any)
		if !ok || len(pair) != 2 {
			continue
		}
		name, _ := pair[0].(string)
		href, ok := pair[1].(string)
		if !ok {
			continue
		}
		if _, seen := byName[name]; !seen {
			order = append(order, name)
		}
		byName[name] = append(byName[name], href)
	}
	return byName, order
}

// domSnapshotJS collects interactive elements with visibility info — the
// fallback both bridges use when the AX tree is too large or mostly ignored.
const domSnapshotJS = `(function() {
  const interactiveSelectors = [
    'a', 'button', 'input', 'textarea', 'select', 'option',
    '[onclick]', '[role="button"]', '[role="link"]',
    '[contenteditable="true"]', 'summary', 'details',
    'a[href]', 'button[type]', 'input[type]',
    'label', 'form', 'nav', 'nav a', 'nav button',
    '[aria-label]', '[aria-labelledby]', '[tabindex]',
    'h1', 'h2', 'h3', 'h4', 'h5', 'h6'
  ].join(', ');
  const elements = document.querySelectorAll(interactiveSelectors);
  const results = [];
  for (const el of elements) {
    const rect = el.getBoundingClientRect();
    const styles = window.getComputedStyle(el);
    if (rect.width === 0 || rect.height === 1 ||
        styles.display === 'none' ||
        styles.visibility === 'hidden' ||
        styles.opacity === '0') {
      continue;
    }
    if (rect.bottom < 0 || rect.top > window.innerHeight ||
        rect.right < 0 || rect.left > window.innerWidth) {
      continue;
    }
    const tag = el.tagName.toLowerCase();
    const text = (el.innerText || el.value || el.placeholder
      || el.getAttribute('aria-label') || '').substring(0, 80);
    const type = el.type || tag;
    const role = el.getAttribute('role') || tag;
    const name = el.name || el.id || '';
    const href = el.href || '';
    const className = el.className || '';
    results.push({
      tag: tag,
      type: type,
      role: role,
      text: text.trim(),
      name: name,
      href: href,
      className: className.split(' ').slice(0, 3).join(' '),
      rect: {
        x: Math.round(rect.x),
        y: Math.round(rect.y),
        width: Math.round(rect.width),
        height: Math.round(rect.height)
      }
    });
  }
  return results;
})()`

// domSnapshot builds the DOM-fallback tree: up to 100 interactive elements,
// one line each, with upstream's e0-based refs and `[href]` marker.
func (b *Bridge) domSnapshot(ctx context.Context, conn *Connection, tabID int) (any, error) {
	v, err := b.evalJS(ctx, conn, tabID, domSnapshotJS, 30*time.Second)
	if err != nil {
		return nil, err
	}
	elements, _ := v.([]any)
	if len(elements) == 0 {
		return map[string]any{
			"ok": true, "tabId": tabID, "tree": "(no visible interactive elements found)",
		}, nil
	}
	var lines []string
	limit := len(elements)
	if limit > 100 {
		limit = 100
	}
	for i := 0; i < limit; i++ {
		el, _ := elements[i].(map[string]any)
		if el == nil {
			continue
		}
		tag, _ := el["tag"].(string)
		if tag == "" {
			tag = "unknown"
		}
		text, _ := el["text"].(string)
		role, _ := el["role"].(string)
		if role == "" {
			role = tag
		}
		href, _ := el["href"].(string)
		desc := role
		if text != "" {
			if len(text) > 40 {
				text = text[:40]
			}
			desc += ` "` + text + `"`
		}
		if href != "" {
			desc += " [href]"
		}
		desc += " [ref=e" + strconv.Itoa(i) + "]"
		lines = append(lines, "  - "+desc)
	}
	url, _ := b.evalJS(ctx, conn, tabID, "window.location.href", 10*time.Second)
	return map[string]any{
		"ok": true, "tabId": tabID, "url": url, "tree": strings.Join(lines, "\n"),
	}, nil
}

// cleanInlineTextBoxes drops InlineTextBox children that only repeat their
// StaticText parent (3+ children, all contained in the parent name).
func cleanInlineTextBoxes(nodes []axNode) []axNode {
	byID := make(map[string]axNode, len(nodes))
	children := make(map[string][]string, len(nodes))
	for _, n := range nodes {
		byID[n.NodeID] = n
		for _, c := range n.ChildIDs {
			children[n.NodeID] = append(children[n.NodeID], c)
		}
	}
	remove := map[string]bool{}
	for _, n := range nodes {
		role, _ := n.Role.Value.(string)
		if role != "StaticText" {
			continue
		}
		childIDs := children[n.NodeID]
		if len(childIDs) < 3 {
			continue
		}
		parentName, _ := n.Name.Value.(string)
		if parentName == "" {
			continue
		}
		allInline := true
		for _, cid := range childIDs {
			child, ok := byID[cid]
			if !ok {
				allInline = false
				break
			}
			childRole, _ := child.Role.Value.(string)
			if childRole != "InlineTextBox" {
				allInline = false
				break
			}
			childName, _ := child.Name.Value.(string)
			if childName != "" && !strings.Contains(parentName, childName) {
				allInline = false
				break
			}
		}
		if allInline {
			for _, cid := range childIDs {
				remove[cid] = true
			}
		}
	}
	if len(remove) == 0 {
		return nodes
	}
	out := make([]axNode, 0, len(nodes))
	for _, n := range nodes {
		if !remove[n.NodeID] {
			out = append(out, n)
		}
	}
	return out
}
