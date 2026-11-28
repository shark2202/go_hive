package astanalyze

// waves computes the port wave and strongly connected component for every
// unit. Edges point from a unit to the units it imports, so a unit comes after
// its dependencies; members of one SCC share a wave and must move together.
func waves(edges [][]int) ([]int, [][]int) {
	type tarjan struct {
		edges   [][]int
		index   []int
		low     []int
		onStack []bool
		stack   []int
		next    int
		comps   [][]int
	}
	n := len(edges)
	t := &tarjan{
		edges:   edges,
		index:   make([]int, n),
		low:     make([]int, n),
		onStack: make([]bool, n),
	}
	for i := range t.index {
		t.index[i] = -1
	}
	var visit func(int)
	visit = func(v int) {
		t.index[v] = t.next
		t.low[v] = t.next
		t.next++
		t.stack = append(t.stack, v)
		t.onStack[v] = true
		for _, w := range t.edges[v] {
			if t.index[w] < 0 {
				visit(w)
				if t.low[w] < t.low[v] {
					t.low[v] = t.low[w]
				}
			} else if t.onStack[w] && t.index[w] < t.low[v] {
				t.low[v] = t.index[w]
			}
		}
		if t.low[v] == t.index[v] {
			var comp []int
			for {
				w := t.stack[len(t.stack)-1]
				t.stack = t.stack[:len(t.stack)-1]
				t.onStack[w] = false
				comp = append(comp, w)
				if w == v {
					break
				}
			}
			t.comps = append(t.comps, comp)
		}
	}
	for v := 0; v < n; v++ {
		if t.index[v] < 0 {
			visit(v)
		}
	}
	wave := make([]int, n)
	members := make([][]int, n)
	for _, comp := range t.comps {
		w := 0
		for _, v := range comp {
			for _, dep := range t.edges[v] {
				if !containsInt(comp, dep) && wave[dep]+1 > w {
					w = wave[dep] + 1
				}
			}
		}
		for _, v := range comp {
			wave[v] = w
			members[v] = comp
		}
	}
	return wave, members
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
