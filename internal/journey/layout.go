package journey

import (
	"cmp"
	"math"
	"slices"
)

// layout places every package once for the whole journey: a bubble whose
// area is its peak size, pulled towards the packages it depends on and
// pushed off the others. It is a force simulation with a fixed start and a
// fixed number of steps, so the same history always gets the same layout.
func layout(j *Journey) {
	n := len(j.Packages)
	if n == 0 {
		return
	}
	r := make([]float64, n)
	for i, p := range j.Packages {
		r[i] = math.Sqrt(float64(max(p.Peak, 1)))
	}

	// Dependencies at any point in the history, weighted by how long and
	// how strongly they held.
	weight := map[[2]int]float64{}
	for _, f := range j.Frames {
		for _, d := range f.Deps {
			a, b := min(d.From, d.To), max(d.From, d.To)
			weight[[2]int{a, b}] += math.Log1p(float64(d.Refs))
		}
	}
	var springs []spring
	maxW := 0.0
	for k, w := range weight {
		springs = append(springs, spring{k[0], k[1], w})
		maxW = max(maxW, w)
	}
	// Map order is random; the sum of forces must not depend on it.
	slices.SortFunc(springs, func(p, q spring) int { return cmp.Or(cmp.Compare(p.a, q.a), cmp.Compare(p.b, q.b)) })

	// Start on a sunflower spiral in path order, so siblings start close.
	x, y := make([]float64, n), make([]float64, n)
	mean := 0.0
	for _, v := range r {
		mean += v
	}
	mean /= float64(n)
	const golden = 2.399963229728653 // radians
	for i := range n {
		d := 2 * mean * math.Sqrt(float64(i)+0.5)
		x[i], y[i] = d*math.Cos(float64(i)*golden), d*math.Sin(float64(i)*golden)
	}

	const steps, gap = 300, 0.6
	for step := range steps {
		cool := 1 - float64(step)/steps
		fx, fy := make([]float64, n), make([]float64, n)
		for _, s := range springs {
			dx, dy := x[s.b]-x[s.a], y[s.b]-y[s.a]
			d := math.Hypot(dx, dy) + 1e-9
			want := r[s.a] + r[s.b] + gap*mean
			f := 0.05 * (s.w / maxW) * (d - want) / d
			fx[s.a] += f * dx
			fy[s.a] += f * dy
			fx[s.b] -= f * dx
			fy[s.b] -= f * dy
		}
		for i := range n {
			// Gravity keeps unconnected packages from drifting off.
			fx[i] -= 0.02 * x[i]
			fy[i] -= 0.02 * y[i]
		}
		for i := range n {
			x[i] += cool * fx[i]
			y[i] += cool * fy[i]
		}
		// Resolve overlaps directly; this is what keeps bubbles apart.
		for i := range n {
			for k := i + 1; k < n; k++ {
				dx, dy := x[k]-x[i], y[k]-y[i]
				d := math.Hypot(dx, dy)
				want := r[i] + r[k] + gap*mean
				if d >= want {
					continue
				}
				if d < 1e-9 {
					// Coincident: separate along a fixed direction.
					dx, dy, d = float64(k-i), 1, math.Hypot(float64(k-i), 1)
				}
				push := (want - d) / d / 2
				x[i] -= dx * push
				y[i] -= dy * push
				x[k] += dx * push
				y[k] += dy * push
			}
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

type spring struct {
	a, b int
	w    float64
}
