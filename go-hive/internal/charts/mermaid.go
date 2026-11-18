package charts

// Mermaid flowchart subset: parse `flowchart`/`graph` sources with rectangle,
// rounded and diamond nodes, plain/labeled edges, and lay the graph out by
// rank (TD/TB/BT vertically, LR/RL horizontally). Other Mermaid diagram types
// are rejected by name rather than drawn as something else.

import (
	"math"
	"regexp"
	"strings"
)

type mermaidNode struct {
	ID    string
	Label string
	Shape string // rect | round | diamond
	Rank  int
	X     float64
	Y     float64
	W     float64
	H     float64
}

type mermaidEdge struct {
	From      string
	To        string
	Label     string
	Arrowhead bool
}

type mermaidGraph struct {
	Direction string
	Nodes     map[string]*mermaidNode
	Order     []string
	Edges     []mermaidEdge
}

var (
	mermaidNodeRe   = regexp.MustCompile(`^\s*([A-Za-z0-9_.:-]+)\s*(\[(.*)\]|\((.*)\)|\{(.*)\})?\s*$`)
	mermaidQuotedRe = regexp.MustCompile(`^"([^"]*)"$`)
)

func parseMermaid(source string) (*mermaidGraph, error) {
	lines := strings.Split(source, "\n")
	direction := ""
	graph := &mermaidGraph{Direction: "TB", Nodes: map[string]*mermaidNode{}}

	for _, raw := range lines {
		line := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), ";"))
		if line == "" || strings.HasPrefix(line, "%%") {
			continue
		}
		if direction == "" {
			fields := strings.Fields(line)
			keyword := strings.ToLower(fields[0])
			if keyword != "flowchart" && keyword != "graph" {
				return nil, &RenderError{Message: "unsupported mermaid diagram " + quote(fields[0]) +
					"; this renderer supports flowchart/graph diagrams"}
			}
			if len(fields) > 1 {
				graph.Direction = strings.ToUpper(fields[1])
			}
			switch graph.Direction {
			case "TD", "TB", "BT", "LR", "RL":
			default:
				return nil, &RenderError{Message: "unsupported flowchart direction " + quote(graph.Direction) +
					"; supported: TD, TB, BT, LR, RL"}
			}
			direction = graph.Direction
			continue
		}

		left, right, arrow, found := splitMermaidEdge(line)
		if !found {
			// A standalone node declaration.
			if node, ok := parseMermaidNode(line); ok {
				graph.addNode(node)
			}
			continue
		}

		edgeLabel := ""
		if strings.HasPrefix(strings.TrimSpace(right), "|") {
			if end := strings.Index(right[1:], "|"); end >= 0 {
				edgeLabel = strings.TrimSpace(right[1 : end+1])
				right = strings.TrimSpace(right[end+2:])
			}
		}
		// `B -- yes --> C` keeps the label on the left side.
		if edgeLabel == "" {
			if cut := strings.LastIndex(left, "--"); cut > 0 {
				candidate := strings.TrimSpace(left[cut+2:])
				nodePart := strings.TrimSpace(left[:cut])
				if candidate != "" && nodePart != "" {
					node, ok := parseMermaidNode(nodePart)
					if ok {
						edgeLabel = candidate
						left = nodePart
						_ = node
					}
				}
			}
		}

		fromNode, fromOK := parseMermaidNode(left)
		toNode, toOK := parseMermaidNode(right)
		if !fromOK || !toOK {
			if node, ok := parseMermaidNode(left); ok {
				graph.addNode(node)
			}
			continue
		}
		graph.addNode(fromNode)
		graph.addNode(toNode)
		graph.Edges = append(graph.Edges, mermaidEdge{
			From:      fromNode.ID,
			To:        toNode.ID,
			Label:     edgeLabel,
			Arrowhead: arrow,
		})
	}
	if direction == "" {
		return nil, &RenderError{Message: "mermaid source does not start with a flowchart/graph declaration"}
	}
	if len(graph.Nodes) == 0 {
		return nil, &RenderError{Message: "mermaid source has no nodes"}
	}
	return graph, nil
}

// splitMermaidEdge splits on the first supported connector.
func splitMermaidEdge(line string) (string, string, bool, bool) {
	for _, token := range []struct {
		text  string
		arrow bool
	}{
		{"-->", true},
		{"-.->", true},
		{"==>", true},
		{"---", false},
	} {
		if index := strings.Index(line, token.text); index >= 0 {
			return strings.TrimSpace(line[:index]), strings.TrimSpace(line[index+len(token.text):]), token.arrow, true
		}
	}
	return "", "", false, false
}

func parseMermaidNode(token string) (*mermaidNode, bool) {
	match := mermaidNodeRe.FindStringSubmatch(strings.TrimSpace(token))
	if match == nil {
		return nil, false
	}
	node := &mermaidNode{ID: match[1], Label: match[1], Shape: "rect"}
	switch {
	case match[3] != "":
		node.Label = cleanMermaidLabel(match[3])
		node.Shape = "rect"
	case match[4] != "":
		node.Label = cleanMermaidLabel(match[4])
		node.Shape = "round"
	case match[5] != "":
		node.Label = cleanMermaidLabel(match[5])
		node.Shape = "diamond"
	}
	return node, true
}

func cleanMermaidLabel(label string) string {
	label = strings.TrimSpace(label)
	if match := mermaidQuotedRe.FindStringSubmatch(label); match != nil {
		return match[1]
	}
	return label
}

func (g *mermaidGraph) addNode(node *mermaidNode) {
	existing, ok := g.Nodes[node.ID]
	if !ok {
		g.Nodes[node.ID] = node
		g.Order = append(g.Order, node.ID)
		return
	}
	// A later declaration may supply the label the first mention lacked.
	if node.Label != node.ID && existing.Label == existing.ID {
		existing.Label = node.Label
		existing.Shape = node.Shape
	}
}

// rankGraph assigns each node the longest distance from a root, tolerating
// cycles by capping the relaxation rounds.
func (g *mermaidGraph) rankGraph() {
	for _, id := range g.Order {
		g.Nodes[id].Rank = 0
	}
	limit := len(g.Order)
	for pass := 0; pass < limit; pass++ {
		changed := false
		for _, edge := range g.Edges {
			from, to := g.Nodes[edge.From], g.Nodes[edge.To]
			if from == nil || to == nil {
				continue
			}
			if to.Rank < from.Rank+1 && to.Rank+1 <= limit {
				to.Rank = from.Rank + 1
				changed = true
			}
		}
		if !changed {
			break
		}
	}
}

func renderMermaidSVG(spec any, width, height, pixelW, pixelH int, theme chartTheme) (string, error) {
	source, ok := spec.(string)
	if !ok {
		return "", &RenderError{Message: "mermaid spec must be a string"}
	}
	graph, err := parseMermaid(source)
	if err != nil {
		return "", err
	}
	graph.rankGraph()

	widthF, heightF := float64(width), float64(height)
	doc := newSVGDoc(pixelW, pixelH, widthF, heightF, theme.Background)

	vertical := graph.Direction != "LR" && graph.Direction != "RL"
	for index, id := range graph.Order {
		node := graph.Nodes[id]
		node.W = math.Min(math.Max(estimateTextWidth(node.Label, 13)+26, 90), 240)
		node.H = 42
		if node.Shape == "diamond" {
			node.H = 58
		}
		_ = index
	}

	rows := map[int][]*mermaidNode{}
	maxRank := 0
	for _, id := range graph.Order {
		node := graph.Nodes[id]
		rows[node.Rank] = append(rows[node.Rank], node)
		if node.Rank > maxRank {
			maxRank = node.Rank
		}
	}

	const margin = 40.0
	if vertical {
		rowCount := maxRank + 1
		gap := 70.0
		if rowCount > 1 {
			gap = (heightF - 2*margin - float64(rowCount)*42) / float64(rowCount-1)
			if gap > 110 {
				gap = 110
			}
			if gap < 36 {
				gap = 36
			}
		}
		for rank := 0; rank <= maxRank; rank++ {
			row := rows[rank]
			total := 0.0
			for _, node := range row {
				total += node.W
			}
			total += 44 * float64(max(len(row)-1, 0))
			x := (widthF - total) / 2
			y := margin + float64(rank)*(42+gap)
			for _, node := range row {
				node.X = x
				node.Y = y
				x += node.W + 44
			}
		}
	} else {
		columnCount := maxRank + 1
		gap := 80.0
		if columnCount > 1 {
			gap = (widthF - 2*margin - float64(columnCount)*120) / float64(columnCount-1)
			if gap > 150 {
				gap = 150
			}
			if gap < 50 {
				gap = 50
			}
		}
		for rank := 0; rank <= maxRank; rank++ {
			column := rows[rank]
			total := 0.0
			for _, node := range column {
				total += node.H
			}
			total += 36 * float64(max(len(column)-1, 0))
			y := (heightF - total) / 2
			x := margin + float64(rank)*(120+gap)
			for _, node := range column {
				node.X = x + (120-node.W)/2
				node.Y = y
				y += node.H + 36
			}
		}
	}

	for _, edge := range graph.Edges {
		drawMermaidEdge(doc, graph, edge, theme)
	}
	for _, id := range graph.Order {
		node := graph.Nodes[id]
		fill := theme.color(indexOf(graph.Order, id))
		switch node.Shape {
		case "diamond":
			cx, cy := node.X+node.W/2, node.Y+node.H/2
			doc.polygon([][2]float64{
				{cx, node.Y}, {node.X + node.W, cy}, {cx, node.Y + node.H}, {node.X, cy},
			}, fill, theme.Background, 1.5)
		case "round":
			doc.rect(node.X, node.Y, node.W, node.H, fill, 14)
			doc.strokeRect(node.X, node.Y, node.W, node.H, theme.Background, 1.5, 14)
		default:
			doc.rect(node.X, node.Y, node.W, node.H, fill, 6)
			doc.strokeRect(node.X, node.Y, node.W, node.H, theme.Background, 1.5, 6)
		}
		textColor := "#ffffff"
		if theme.Name == "dark" {
			textColor = theme.Background
		}
		doc.text(node.X+node.W/2, node.Y+node.H/2+4, truncateLabel(node.Label, 30), "middle", textColor, 13, 500)
	}
	return doc.String(), nil
}

func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return 0
}

func drawMermaidEdge(doc *svgDoc, graph *mermaidGraph, edge mermaidEdge, theme chartTheme) {
	from, to := graph.Nodes[edge.From], graph.Nodes[edge.To]
	if from == nil || to == nil {
		return
	}
	dx := (to.X + to.W/2) - (from.X + from.W/2)
	dy := (to.Y + to.H/2) - (from.Y + from.H/2)
	length := math.Hypot(dx, dy)
	if length == 0 {
		return
	}
	ux, uy := dx/length, dy/length
	x1, y1 := clipToBox(from.X+from.W/2, from.Y+from.H/2, from.W, from.H, ux, uy)
	x2, y2 := clipToBox(to.X+to.W/2, to.Y+to.H/2, to.W, to.H, -ux, -uy)
	doc.line(x1, y1, x2, y2, theme.Muted, 1.5, "")
	if edge.Arrowhead {
		head := 9.0
		angle := math.Atan2(y2-y1, x2-x1)
		left := [2]float64{x2 - head*math.Cos(angle-0.42), y2 - head*math.Sin(angle-0.42)}
		right := [2]float64{x2 - head*math.Cos(angle+0.42), y2 - head*math.Sin(angle+0.42)}
		doc.polygon([][2]float64{{x2, y2}, left, right}, theme.Muted, "none", 0)
	}
	if edge.Label != "" {
		mx, my := (x1+x2)/2, (y1+y2)/2
		labelWidth := estimateTextWidth(edge.Label, 11) + 10
		doc.rect(mx-labelWidth/2, my-10, labelWidth, 18, theme.Background, 4)
		doc.text(mx, my+4, truncateLabel(edge.Label, 24), "middle", theme.Muted, 11, 400)
	}
}

// clipToBox returns the point where a ray from a box centre leaves the box.
func clipToBox(cx, cy, width, height, dx, dy float64) (float64, float64) {
	halfW, halfH := width/2, height/2
	tx, ty := math.Inf(1), math.Inf(1)
	if dx != 0 {
		tx = halfW / math.Abs(dx)
	}
	if dy != 0 {
		ty = halfH / math.Abs(dy)
	}
	t := math.Min(tx, ty)
	if math.IsInf(t, 1) {
		return cx, cy
	}
	return cx + t*dx, cy + t*dy
}
