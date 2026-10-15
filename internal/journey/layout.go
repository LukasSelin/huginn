package journey

import (
	"cmp"
	"container/heap"
	"maps"
	"math"
	"path"
	"slices"
)

// layout places every package once for the whole journey: a bubble whose
// area is its peak size, placed so that the distance between two packages
// follows how tightly they are coupled and how close they sit in the
// directory tree.
//
// Coupling is taken relative to how coupled each side is overall, so a
// package that everything imports, like a logger, does not pull everything
// together. The directory tree enters as hidden nodes, one per directory,
// that link each package to its parent; siblings that never import each
// other still keep together.
//
// It is stress majorization over shortest-path distances in that graph,
// from a classical scaling start and for a fixed number of steps, so the
// same history always gets the same layout.
func layout(j *Journey, coupling []spring) {
	n := len(j.Packages)
	if n == 0 {
		return
	}
	r := make([]float64, n)
	mean := 0.0
	for i, p := range j.Packages {
		r[i] = math.Sqrt(float64(max(p.Peak, 1)))
		mean += r[i]
	}
	mean /= float64(n)
	const gap = 0.6
	clear := func(a, b int) float64 { return r[a] + r[b] + gap*mean }

	// Nodes past n are directories that are not packages themselves.
	node := map[string]int{}
	for i, p := range j.Packages {
		node[p.Path] = i
	}
	var adj [][]spring
	link := func(a, b int, l float64) {
		for len(adj) <= max(a, b) {
			adj = append(adj, nil)
		}
		adj[a] = append(adj[a], spring{a, b, l})
		adj[b] = append(adj[b], spring{b, a, l})
	}
	radius := func(i int) float64 {
		if i < n {
			return r[i]
		}
		return 0
	}
	dirs := slices.Sorted(maps.Keys(node))
	for k := 0; k < len(dirs); k++ {
		p := dirs[k]
		if p == "" {
			continue
		}
		parent := path.Dir(p)
		if parent == "." {
			parent = ""
		}
		pi, ok := node[parent]
		if !ok {
			pi = len(node)
			node[parent] = pi
			dirs = append(dirs, parent)
		}
		link(node[p], pi, radius(node[p])+radius(pi)+1.5*mean)
	}
	total := len(node)
	for len(adj) < total {
		adj = append(adj, nil)
	}

	// Relative coupling: a pair's weight over the geometric mean of each
	// side's total. The tightest pair is wanted just clear of each other,
	// the loosest a few bubbles further.
	sum := make([]float64, n)
	for _, s := range coupling {
		sum[s.a] += s.w
		sum[s.b] += s.w
	}
	rel := make([]float64, len(coupling))
	maxRel := 0.0
	for k, s := range coupling {
		rel[k] = s.w / math.Sqrt(sum[s.a]*sum[s.b])
		maxRel = max(maxRel, rel[k])
	}
	for k, s := range coupling {
		link(s.a, s.b, clear(s.a, s.b)+mean*(0.5+3*(1-math.Sqrt(rel[k]/maxRel))))
	}

	d := distances(adj)
	for i := range total {
		for k := range total {
			if math.IsInf(d[i][k], 1) {
				// Cannot happen with the root linking every directory, but
				// keep the scaling finite regardless.
				d[i][k] = 10 * mean * math.Sqrt(float64(total))
			}
			if i != k && i < n && k < n {
				// A path can be shorter than the bubbles at its ends.
				d[i][k] = max(d[i][k], clear(i, k))
			}
		}
	}

	x, y := classical(d)

	// Each step moves every node to where its distances to all the
	// others, weighted by 1/d², are best met given where the others are.
	nx, ny := make([]float64, total), make([]float64, total)
	for range 300 {
		for i := range total {
			sx, sy, sw := 0.0, 0.0, 0.0
			for k := range total {
				if k == i || d[i][k] == 0 {
					continue
				}
				w := 1 / (d[i][k] * d[i][k])
				dx, dy := x[i]-x[k], y[i]-y[k]
				l := math.Hypot(dx, dy)
				if l < 1e-9 {
					// Coincident: separate along a fixed direction.
					dx, dy, l = float64(i-k), 1, math.Hypot(float64(i-k), 1)
				}
				sx += w * (x[k] + d[i][k]*dx/l)
				sy += w * (y[k] + d[i][k]*dy/l)
				sw += w
			}
			if sw == 0 {
				nx[i], ny[i] = x[i], y[i]
				continue
			}
			nx[i], ny[i] = sx/sw, sy/sw
		}
		x, nx = nx, x
		y, ny = ny, y
	}
	x, y = x[:n], y[:n]

	// The distances are met only on average, and bubbles in the dense
	// middle overlap. Pushing them apart from there would spread the middle
	// evenly and lose its structure, so first scale the whole layout up
	// until most bubbles clear their neighbours.
	need := make([]float64, n)
	for i := range n {
		for k := range n {
			if k != i {
				need[i] = max(need[i], clear(i, k)/max(math.Hypot(x[i]-x[k], y[i]-y[k]), 1e-9))
			}
		}
	}
	slices.Sort(need)
	scale := max(1, need[n/2])
	for i := range n {
		x[i] *= scale
		y[i] *= scale
	}

	// Distances are only met on average; push any bubbles still overlapping
	// apart until none do.
	for range 500 {
		if !separate(x, y, clear) {
			break
		}
	}

	// The packed middle cannot shrink, but the loosely tied packages around
	// it leave gaps that would make every bubble small once the layout is
	// fitted to the stage. Draw everything in, a little at a time, keeping
	// bubbles apart, so the outskirts close up around the middle without
	// changing who sits next to whom.
	cx, cy := 0.0, 0.0
	for i := range n {
		cx += x[i]
		cy += y[i]
	}
	cx /= float64(n)
	cy /= float64(n)
	area := 0.0
	for i := range n {
		area += (r[i] + gap*mean/2) * (r[i] + gap*mean/2)
	}
	for range 50 {
		// Stop once the bubbles fill enough of the disc around them;
		// closer than that only crowds unrelated packages together.
		reach := 0.0
		for i := range n {
			reach = max(reach, math.Hypot(x[i]-cx, y[i]-cy)+r[i])
		}
		if area/(reach*reach) >= 0.4 {
			break
		}
		for i := range n {
			x[i] = cx + (x[i]-cx)*0.97
			y[i] = cy + (y[i]-cy)*0.97
		}
		for range 20 {
			if !separate(x, y, clear) {
				break
			}
		}
	}
	for range 500 {
		if !separate(x, y, clear) {
			break
		}
	}

	// Fit the bounding box, bubbles included, into the unit square.
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for i := range n {
		x0, y0 = min(x0, x[i]-r[i]), min(y0, y[i]-r[i])
		x1, y1 = max(x1, x[i]+r[i]), max(y1, y[i]+r[i])
	}
	size := max(x1-x0, y1-y0)
	for i := range j.Packages {
		p := &j.Packages[i]
		p.X = (x[i] - x0 + (size-(x1-x0))/2) / size
		p.Y = (y[i] - y0 + (size-(y1-y0))/2) / size
		p.R = r[i] / size
		if p.Peak == 0 {
			p.R = 0
		}
	}
}

// separate pushes every pair of bubbles that are closer than clear asks
// apart, half each, and reports whether it moved any.
func separate(x, y []float64, clear func(a, b int) float64) bool {
	moved := false
	for i := range x {
		for k := i + 1; k < len(x); k++ {
			dx, dy := x[k]-x[i], y[k]-y[i]
			l := math.Hypot(dx, dy)
			want := clear(i, k)
			if l >= want {
				continue
			}
			if l < 1e-9 {
				// Coincident: separate along a fixed direction.
				dx, dy, l = float64(k-i), 1, math.Hypot(float64(k-i), 1)
			}
			push := (want - l) / l / 2
			x[i] -= dx * push
			y[i] -= dy * push
			x[k] += dx * push
			y[k] += dy * push
			moved = true
		}
	}
	return moved
}

// spring links packages a and b with weight w: their coupling, or, once
// laid out, the length it asks for.
type spring struct {
	a, b int
	w    float64
}

// sortSprings sorts by a then b. Springs come out of maps in random order,
// and the layout must not depend on it.
func sortSprings(s []spring) {
	slices.SortFunc(s, func(p, q spring) int { return cmp.Or(cmp.Compare(p.a, q.a), cmp.Compare(p.b, q.b)) })
}

// distances returns the shortest path lengths between all nodes over adj,
// +Inf where there is none.
func distances(adj [][]spring) [][]float64 {
	n := len(adj)
	d := make([][]float64, n)
	for src := range n {
		dist := make([]float64, n)
		for i := range dist {
			dist[i] = math.Inf(1)
		}
		dist[src] = 0
		q := &queue{{src, 0}}
		for q.Len() > 0 {
			it := heap.Pop(q).(item)
			if it.d > dist[it.i] {
				continue
			}
			for _, s := range adj[it.i] {
				if nd := it.d + s.w; nd < dist[s.b] {
					dist[s.b] = nd
					heap.Push(q, item{s.b, nd})
				}
			}
		}
		d[src] = dist
	}
	return d
}

type item struct {
	i int
	d float64
}

type queue []item

func (q queue) Len() int           { return len(q) }
func (q queue) Less(a, b int) bool { return q[a].d < q[b].d || q[a].d == q[b].d && q[a].i < q[b].i }
func (q queue) Swap(a, b int)      { q[a], q[b] = q[b], q[a] }
func (q *queue) Push(x any)        { *q = append(*q, x.(item)) }
func (q *queue) Pop() any {
	old := *q
	it := old[len(old)-1]
	*q = old[:len(old)-1]
	return it
}

// classical returns the two-dimensional classical scaling of the distances
// d: the top two eigenvectors of the double-centred squared distances, by
// power iteration from a fixed start. It gets the global shape right, which
// stress majorization alone, being local, often does not.
func classical(d [][]float64) (x, y []float64) {
	n := len(d)
	b := make([][]float64, n)
	row := make([]float64, n)
	all := 0.0
	for i := range n {
		b[i] = make([]float64, n)
		for k := range n {
			b[i][k] = d[i][k] * d[i][k]
			row[i] += b[i][k]
		}
		all += row[i]
		row[i] /= float64(n)
	}
	all /= float64(n * n)
	for i := range n {
		for k := range n {
			b[i][k] = -(b[i][k] - row[i] - row[k] + all) / 2
		}
	}
	var vecs [2][]float64
	var vals [2]float64
	for e := range 2 {
		v := make([]float64, n)
		for i := range v {
			// Any start not orthogonal to the answer works; this one is
			// fixed and uneven.
			v[i] = math.Sin(float64(i*(e+1)) + 1)
		}
		val := 0.0
		for range 100 {
			w := make([]float64, n)
			for i := range n {
				for k := range n {
					w[i] += b[i][k] * v[k]
				}
			}
			if e == 1 {
				// Deflate the first eigenvector.
				dot := 0.0
				for i := range n {
					dot += vecs[0][i] * v[i]
				}
				for i := range n {
					w[i] -= vals[0] * dot * vecs[0][i]
				}
			}
			norm := 0.0
			for _, f := range w {
				norm += f * f
			}
			norm = math.Sqrt(norm)
			if norm < 1e-12 {
				break
			}
			for i := range n {
				v[i] = w[i] / norm
			}
			val = norm
		}
		vecs[e], vals[e] = v, val
	}
	x, y = make([]float64, n), make([]float64, n)
	for i := range n {
		x[i] = vecs[0][i] * math.Sqrt(vals[0])
		y[i] = vecs[1][i] * math.Sqrt(vals[1])
	}
	return x, y
}
