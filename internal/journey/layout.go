package journey

import (
	"math"
)

// layout places every package once for the whole journey: a bubble whose
// area is its peak size, placed so that the distance between two packages
// follows how tightly they are coupled. Packages that import each other and
// share many references sit side by side; packages linked only through
// others sit as far apart as that chain is long; unrelated ones keep to the
// edge.
//
// It is stress majorization over coupling distances, from a fixed start
// and for a fixed number of steps, so the same history always gets the
// same layout.
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

	// The tightest pair is wanted just clear of each other, the loosest
	// a few bubbles further.
	maxW := 0.0
	for _, s := range coupling {
		maxW = max(maxW, s.w)
	}
	adj := make([][]spring, n)
	for _, s := range coupling {
		s.w = clear(s.a, s.b) + 3*mean*(1-s.w/maxW)
		adj[s.a] = append(adj[s.a], s)
		adj[s.b] = append(adj[s.b], spring{s.b, s.a, s.w})
	}
	d := distances(adj)
	far := 0.0
	for i := range n {
		for k := range n {
			if !math.IsInf(d[i][k], 1) {
				far = max(far, d[i][k])
			}
		}
	}
	for i := range n {
		for k := range n {
			if math.IsInf(d[i][k], 1) {
				d[i][k] = max(far, clear(i, k)) + 2*mean
			}
			if i != k {
				// A path can be shorter than the bubbles at its ends.
				d[i][k] = max(d[i][k], clear(i, k))
			}
		}
	}

	// Start on a sunflower spiral in path order, so siblings start close.
	x, y := make([]float64, n), make([]float64, n)
	const golden = 2.399963229728653 // radians
	for i := range n {
		s := 2 * mean * math.Sqrt(float64(i)+0.5)
		x[i], y[i] = s*math.Cos(float64(i)*golden), s*math.Sin(float64(i)*golden)
	}

	// Each step moves every package to where its distances to all the
	// others, weighted by 1/d², are best met given where the others are.
	nx, ny := make([]float64, n), make([]float64, n)
	for range 300 {
		for i := range n {
			sx, sy, sw := 0.0, 0.0, 0.0
			for k := range n {
				if k == i {
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

	// Distances are only met on average; push any bubbles still overlapping
	// apart until none do.
	for range 500 {
		moved := false
		for i := range n {
			for k := i + 1; k < n; k++ {
				dx, dy := x[k]-x[i], y[k]-y[i]
				l := math.Hypot(dx, dy)
				want := clear(i, k)
				if l >= want {
					continue
				}
				if l < 1e-9 {
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
		if !moved {
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

// spring links packages a and b with weight w: their coupling, or, once
// laid out, the length it asks for.
type spring struct {
	a, b int
	w    float64
}

// distances returns the shortest path lengths between all packages over
// adj, +Inf where there is none. Packages are few and links sparse, so it
// runs a simple Dijkstra from each.
func distances(adj [][]spring) [][]float64 {
	n := len(adj)
	d := make([][]float64, n)
	for src := range n {
		dist := make([]float64, n)
		for i := range dist {
			dist[i] = math.Inf(1)
		}
		dist[src] = 0
		done := make([]bool, n)
		for {
			u := -1
			for i := range n {
				if !done[i] && !math.IsInf(dist[i], 1) && (u < 0 || dist[i] < dist[u]) {
					u = i
				}
			}
			if u < 0 {
				break
			}
			done[u] = true
			for _, s := range adj[u] {
				dist[s.b] = min(dist[s.b], dist[u]+s.w)
			}
		}
		d[src] = dist
	}
	return d
}
