package codegraph

import "sort"

// PersonalizedRank computes deterministic 20-iteration damped Personalized
// PageRank (d = 0.85) biased toward seeds (map[nodeID]weight). Unrecognized
// seed IDs are ignored; if no valid seeds are supplied, uniform weights are used.
func (g *Graph) PersonalizedRank(seeds map[string]float64) map[string]float64 {
	n := len(g.Nodes)
	out := make(map[string]float64, n)
	if n == 0 {
		return out
	}

	idxByID := make(map[string]int, n)
	ids := make([]string, n)
	for i, nd := range g.Nodes {
		ids[i] = nd.ID
	}
	sort.Strings(ids)
	for i, id := range ids {
		idxByID[id] = i
	}

	pers := make([]float64, n)
	totalSeed := 0.0
	for id, w := range seeds {
		if w <= 0 {
			continue
		}
		if idx, ok := idxByID[id]; ok {
			pers[idx] += w
			totalSeed += w
		}
	}
	if totalSeed == 0 {
		uniform := 1.0 / float64(n)
		for i := range pers {
			pers[i] = uniform
		}
	} else {
		for i := range pers {
			pers[i] /= totalSeed
		}
	}

	type neighbor struct {
		to     int
		weight float64
	}
	adj := make([][]neighbor, n)
	outSum := make([]float64, n)
	for _, e := range g.Edges {
		u, okU := idxByID[e.From]
		v, okV := idxByID[e.To]
		if !okU || !okV || u == v {
			continue
		}
		fwd := edgeWeight(e.Relation, true)
		rev := edgeWeight(e.Relation, false)
		adj[u] = append(adj[u], neighbor{to: v, weight: fwd})
		outSum[u] += fwd
		adj[v] = append(adj[v], neighbor{to: u, weight: rev})
		outSum[v] += rev
	}

	rank := append([]float64(nil), pers...)
	const damping = 0.85
	for iter := 0; iter < 20; iter++ {
		next := make([]float64, n)
		dangling := 0.0
		for u := 0; u < n; u++ {
			if outSum[u] == 0 {
				dangling += rank[u]
				continue
			}
			share := damping * rank[u] / outSum[u]
			for _, nb := range adj[u] {
				next[nb.to] += share * nb.weight
			}
		}
		for i := 0; i < n; i++ {
			next[i] += (1.0 - damping + damping*dangling) * pers[i]
		}
		rank = next
	}

	for i, id := range ids {
		out[id] = rank[i]
	}
	return out
}

func edgeWeight(rel string, forward bool) float64 {
	if forward {
		switch rel {
		case "CALLS", "TESTS", "USES_TYPE", "IMPORTS":
			return 1.0
		case "DEFINES":
			return 0.55
		default:
			return 0.65
		}
	}
	switch rel {
	case "CALLS", "TESTS", "USES_TYPE", "DOCUMENTS":
		return 0.85
	case "DEFINES":
		return 0.70
	default:
		return 0.55
	}
}
