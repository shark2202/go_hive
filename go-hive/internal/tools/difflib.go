package tools

func ratio(a, b string) float64 {
	ar := []rune(a)
	br := []rune(b)
	total := len(ar) + len(br)
	if total == 0 {
		return 1.0
	}
	matches := 0
	for _, block := range matchingBlocks(ar, br) {
		matches += block.size
	}
	return 2.0 * float64(matches) / float64(total)
}

type matchBlock struct {
	a, b, size int
}

func matchingBlocks(a, b []rune) []matchBlock {
	b2j := map[rune][]int{}
	for i, r := range b {
		b2j[r] = append(b2j[r], i)
	}
	var blocks []matchBlock
	type region struct{ alo, ahi, blo, bhi int }
	regions := []region{{0, len(a), 0, len(b)}}
	for len(regions) > 0 {
		reg := regions[len(regions)-1]
		regions = regions[:len(regions)-1]
		ai, bi, size := longestMatch(a, b, reg.alo, reg.ahi, reg.blo, reg.bhi, b2j)
		if size == 0 {
			continue
		}
		blocks = append(blocks, matchBlock{a: ai, b: bi, size: size})
		if reg.alo < ai && reg.blo < bi {
			regions = append(regions, region{reg.alo, ai, reg.blo, bi})
		}
		if ai+size < reg.ahi && bi+size < reg.bhi {
			regions = append(regions, region{ai + size, reg.ahi, bi + size, reg.bhi})
		}
	}
	return blocks
}

func longestMatch(a, b []rune, alo, ahi, blo, bhi int, b2j map[rune][]int) (int, int, int) {
	besti, bestj, bestsize := alo, blo, 0
	j2len := map[int]int{}
	for i := alo; i < ahi; i++ {
		newj2len := map[int]int{}
		for _, j := range b2j[a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			k := j2len[j-1] + 1
			newj2len[j] = k
			if k > bestsize {
				besti, bestj, bestsize = i-k+1, j-k+1, k
			}
		}
		j2len = newj2len
	}
	for besti > alo && bestj > blo && a[besti-1] == b[bestj-1] {
		besti, bestj, bestsize = besti-1, bestj-1, bestsize+1
	}
	for besti+bestsize < ahi && bestj+bestsize < bhi && a[besti+bestsize] == b[bestj+bestsize] {
		bestsize++
	}
	return besti, bestj, bestsize
}

func GetCloseMatches(word string, possibilities []string, n int, cutoff float64) []string {
	if n <= 0 {
		return []string{}
	}
	if cutoff < 0 || cutoff > 1 {
		return []string{}
	}
	type scored struct {
		score float64
		name  string
	}
	var hits []scored
	wordRunes := []rune(word)
	for _, candidate := range possibilities {
		if realQuickRatio(candidate, wordRunes) < cutoff {
			continue
		}
		if quickRatio(candidate, wordRunes) < cutoff {
			continue
		}
		score := ratio(candidate, word)
		if score >= cutoff {
			hits = append(hits, scored{score: score, name: candidate})
		}
	}
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0; j-- {
			left, right := hits[j-1], hits[j]
			if left.score < right.score || (left.score == right.score && left.name < right.name) {
				hits[j-1], hits[j] = hits[j], hits[j-1]
			} else {
				break
			}
		}
	}
	if len(hits) > n {
		hits = hits[:n]
	}
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.name
	}
	return out
}

func realQuickRatio(a string, bRunes []rune) float64 {
	la, lb := len([]rune(a)), len(bRunes)
	if la+lb == 0 {
		return 1
	}
	return 2.0 * float64(minInt(la, lb)) / float64(la+lb)
}

func quickRatio(a string, bRunes []rune) float64 {
	aRunes := []rune(a)
	bSet := map[rune]struct{}{}
	for _, r := range bRunes {
		bSet[r] = struct{}{}
	}
	aSet := map[rune]struct{}{}
	for _, r := range aRunes {
		aSet[r] = struct{}{}
	}
	common := 0
	for r := range aSet {
		if _, ok := bSet[r]; ok {
			common++
		}
	}
	total := len(aSet) + len(bSet)
	if total == 0 {
		return 1
	}
	return 2.0 * float64(common) / float64(total)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
