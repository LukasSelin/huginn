package journey

import (
	"cmp"
	"container/heap"
	"math"
	"slices"
	"strings"
)

// Group is a directory holding more than one package or package directory
// below it: a cluster on the map that its packages sit inside, and that
// dependencies between packages of different clusters are bundled through.
type Group struct {
	Path   string
	Parent int // index into Journey.Groups, or -1 for the outermost

	// X, Y and R place the cluster's circle in the unit square, like a
	// package's bubble.
	X, Y, R float64
}

// layout places every package once for the whole journey, a bubble whose
// area is its peak size, and every group around its packages.
//
// The directory tree decides the clusters: each directory lays out its
// subdirectories and its own package side by side, so that a package's
// neighbours are first its siblings, and a subtree stays together however
// large the repository grows. Within a directory, the distance between two
// children follows how tightly the packages under them are coupled:
// packages that import each other and share many references sit side by
// side; those linked only through others sit as far apart as that chain is
// long; unrelated ones are packed around them. Each cluster is then turned
// to face the clusters it is coupled to, so that dependencies between them
// stay short.
//
// Each directory only lays out its own children, so a repository of
// thousands of packages costs little more than its largest directory.
//
// Every step is deterministic, so the same history always gets the same
// layout.
func layout(j *Journey, coupling []spring) {
	n := len(j.Packages)
	if n == 0 {
		return
	}
	root := tree(j.Packages)
	leaves := make([]*node, n)
	for _, l := range root.leaves(nil) {
		leaves[l.pkg] = l
	}
	for _, s := range coupling {
		couple(leaves[s.a], leaves[s.b], s)
	}
	root.arrange()

	// Turn each cluster, outermost first, to face what it is coupled to
	// outside it, and place everything absolutely.
	x, y := make([]float64, n), make([]float64, n)
	groups := root.groups()
	for _, g := range groups {
		if g != root {
			g.face(root, x, y)
		}
	}
	root.place(0, 0, x, y)

	// Fit the outermost circle into the unit square.
	size := 2 * root.r
	fit := func(v float64) float64 { return (v + root.r) / size }
	j.Groups = nil
	gi := map[*node]int{}
	for _, g := range groups {
		parent := -1
		if g.parent != nil {
			parent = gi[g.parent]
		}
		gi[g] = len(j.Groups)
		j.Groups = append(j.Groups, Group{Path: g.path, Parent: parent, X: fit(g.ax), Y: fit(g.ay), R: g.r / size})
	}
	for _, l := range leaves {
		p := &j.Packages[l.pkg]
		p.X, p.Y, p.R = fit(l.ax), fit(l.ay), l.r/size
		if p.Peak == 0 {
			p.R = 0
		}
		p.Group = -1
		if l.parent != nil {
			p.Group = gi[l.parent]
		}
	}
}

// node is a package (a leaf) or a group of the layout's directory tree.
type node struct {
	path   string
	pkg    int // index into Journey.Packages for a leaf, else -1
	parent *node
	kids   []*node
	depth  int

	r      float64 // radius
	x, y   float64 // center, relative to the parent's
	ax, ay float64 // center on the map, once placed

	// pairs couples the kids by index: the springs between the packages
	// under them, summed. out lists, per spring leaving the group, which
	// kid it leaves from and the package at its other end.
	pairs map[[2]int]float64
	out   []exit
}

type exit struct {
	kid, pkg int
	w        float64
}

// minSizes returns, for a map of n packages, the fewest packages a directory
// needs below it to be a group, and the fewest children. A circle around a
// few others is mostly padding; and the larger the map, the more of it is
// laid out by coupling within a group rather than split by directory, so
// that what is coupled sits close. On moby's 3,767 packages a group holds
// 40 packages or more.
func minSizes(n int) (pkgs, kids int) {
	pkgs = max(5, int(math.Sqrt(float64(n))*2/3))
	return pkgs, max(4, pkgs/5)
}

// tree arranges the packages by directory. Directories with a single child
// are folded into it, and those too small by minSizes into their parent,
// so every group left has two children or more; a directory that is itself
// a package as well as a parent holds its own package as a leaf among its
// subdirectories.
func tree(pkgs []Package) *node {
	minGroup, minKids := minSizes(len(pkgs))
	dirs := map[string]*node{}
	var dir func(p string) *node
	dir = func(p string) *node {
		if d := dirs[p]; d != nil {
			return d
		}
		d := &node{path: p, pkg: -1}
		dirs[p] = d
		if p != "." {
			up := "."
			if i := strings.LastIndexByte(p, '/'); i >= 0 {
				up = p[:i]
			}
			parent := dir(up)
			parent.kids = append(parent.kids, d)
		}
		return d
	}
	root := dir(".")
	for i, p := range pkgs {
		d := dir(p.Path)
		d.kids = append(d.kids, &node{path: p.Path, pkg: i, r: math.Sqrt(float64(max(p.Peak, 1)))})
	}
	// fold returns what d leaves its parent: d itself, or, for a directory
	// too small to be a group, what is in it. It counts the packages under
	// d in size.
	size := map[*node]int{}
	var fold func(d *node) []*node
	fold = func(d *node) []*node {
		if d.pkg >= 0 {
			size[d] = 1
			return []*node{d}
		}
		var kids []*node
		for _, k := range d.kids {
			for _, f := range fold(k) {
				kids = append(kids, f)
				size[d] += size[f]
			}
		}
		d.kids = kids
		if len(kids) == 1 || d != root && (size[d] < minGroup || len(kids) < minKids) {
			return kids
		}
		// The package of the directory itself first, then its
		// subdirectories by path.
		own := func(k *node) int {
			if k.pkg >= 0 && k.path == d.path {
				return 0
			}
			return 1
		}
		slices.SortFunc(d.kids, func(a, b *node) int { return cmp.Or(cmp.Compare(own(a), own(b)), cmp.Compare(a.path, b.path)) })
		return []*node{d}
	}
	root = fold(root)[0]
	var link func(d *node)
	link = func(d *node) {
		for _, k := range d.kids {
			k.parent, k.depth = d, d.depth+1
			link(k)
		}
	}
	link(root)
	return root
}

// leaves appends the packages under d, in tree order.
func (d *node) leaves(out []*node) []*node {
	if d.pkg >= 0 {
		return append(out, d)
	}
	for _, k := range d.kids {
		out = k.leaves(out)
	}
	return out
}

// groups returns d and the groups under it, outermost first.
func (d *node) groups() []*node {
	var out []*node
	queue := []*node{d}
	for len(queue) > 0 {
		g := queue[0]
		queue = queue[1:]
		if g.pkg >= 0 {
			continue
		}
		out = append(out, g)
		queue = append(queue, g.kids...)
	}
	return out
}

// couple adds spring s between the packages at leaves a and b to the group
// where they part ways, and to every group between there and either package
// as a spring leaving it.
func couple(a, b *node, s spring) {
	if a == b {
		return
	}
	for a.depth > b.depth {
		a.parent.exit(a, s.b, s.w)
		a = a.parent
	}
	for b.depth > a.depth {
		b.parent.exit(b, s.a, s.w)
		b = b.parent
	}
	for a.parent != b.parent {
		a.parent.exit(a, s.b, s.w)
		b.parent.exit(b, s.a, s.w)
		a, b = a.parent, b.parent
	}
	g := a.parent
	if g == nil {
		return
	}
	ka, kb := slices.Index(g.kids, a), slices.Index(g.kids, b)
	if g.pairs == nil {
		g.pairs = map[[2]int]float64{}
	}
	g.pairs[[2]int{min(ka, kb), max(ka, kb)}] += s.w
}

func (g *node) exit(kid *node, pkg int, w float64) {
	g.out = append(g.out, exit{slices.Index(g.kids, kid), pkg, w})
}

// arrange lays out the groups under d and then d's kids, sizing d to hold
// them.
func (d *node) arrange() {
	if d.pkg >= 0 {
		return
	}
	for _, k := range d.kids {
		k.arrange()
	}
	m := len(d.kids)
	r := make([]float64, m)
	mean := 0.0
	for i, k := range d.kids {
		r[i] = k.r
		mean += k.r
	}
	mean /= float64(m)
	springs := make([]spring, 0, len(d.pairs))
	for k, w := range d.pairs {
		springs = append(springs, spring{k[0], k[1], w})
	}
	slices.SortFunc(springs, func(p, q spring) int { return cmp.Or(cmp.Compare(p.a, q.a), cmp.Compare(p.b, q.b)) })
	x, y := stress(r, mean, springs)

	// Center the children in the smallest circle around them, then leave a
	// rim, so that a group reads as one.
	cx, cy, R := enclose(x, y, r)
	for i, k := range d.kids {
		k.x, k.y = x[i]-cx, y[i]-cy
	}
	d.r = R + 0.04*R + 0.3*slices.Min(r)
	d.pairs = nil
}

// face turns group g about its center, and mirrors it if that helps, so that
// its children sit nearest the packages outside it they are coupled to.
// Those are taken where they are placed so far: the groups are turned
// outermost first.
func (g *node) face(root *node, x, y []float64) {
	if len(g.out) == 0 {
		return
	}
	root.place(0, 0, x, y)
	px, py := make([]float64, len(g.kids)), make([]float64, len(g.kids))
	for i, k := range g.kids {
		px[i], py[i] = k.x, k.y
	}
	best, bestCost := [2]int{0, 0}, math.Inf(1)
	const turns = 24
	for flip := range 2 {
		for t := range turns {
			sin, cos := math.Sincos(2 * math.Pi * float64(t) / turns)
			cost := 0.0
			for _, e := range g.out {
				kx, ky := px[e.kid], py[e.kid]
				if flip == 1 {
					kx = -kx
				}
				kx, ky = kx*cos-ky*sin, kx*sin+ky*cos
				cost += e.w * math.Hypot(g.ax+kx-x[e.pkg], g.ay+ky-y[e.pkg])
			}
			// Ties keep the turn found first, so the result is stable.
			if cost < bestCost-1e-9 {
				best, bestCost = [2]int{flip, t}, cost
			}
		}
	}
	sin, cos := math.Sincos(2 * math.Pi * float64(best[1]) / turns)
	g.turn(best[0] == 1, sin, cos)
	g.out = nil
}

// turn mirrors and rotates everything under d about d's center.
func (d *node) turn(flip bool, sin, cos float64) {
	for _, k := range d.kids {
		if flip {
			k.x = -k.x
		}
		k.x, k.y = k.x*cos-k.y*sin, k.x*sin+k.y*cos
		k.turn(flip, sin, cos)
	}
}

// place puts d at (x0, y0) and everything under it relative to that,
// recording where the packages went in x and y.
func (d *node) place(x0, y0 float64, x, y []float64) {
	d.ax, d.ay = x0, y0
	if d.pkg >= 0 {
		x[d.pkg], y[d.pkg] = x0, y0
		return
	}
	for _, k := range d.kids {
		k.place(x0+k.x, y0+k.y, x, y)
	}
}

// gap is the room left between two circles, relative to the smaller.
const gap = 0.6

// stress places circles of radii r, whose mean is mean, so that their
// distances follow the springs between them. Each set of circles coupled to
// one another, directly or not, is laid out on its own; the sets are then
// packed around the largest, so that unrelated packages keep to the edge
// without leaving room for a coupling they do not have. Last, all are drawn
// together, so that the springs set the arrangement but leave no slack.
func stress(r []float64, mean float64, coupling []spring) (x, y []float64) {
	n := len(r)
	maxW := 0.0
	for _, s := range coupling {
		maxW = max(maxW, s.w)
	}

	// Number the sets by their first circle.
	set := make([]int, n)
	for i := range set {
		set[i] = i
	}
	var find func(i int) int
	find = func(i int) int {
		if set[i] != i {
			set[i] = find(set[i])
		}
		return set[i]
	}
	for _, s := range coupling {
		a, b := find(s.a), find(s.b)
		set[max(a, b)] = min(a, b)
	}
	var sets [][]int
	at := map[int]int{}
	for i := range n {
		f := find(i)
		k, ok := at[f]
		if !ok {
			k = len(sets)
			at[f] = k
			sets = append(sets, nil)
		}
		sets[k] = append(sets[k], i)
	}
	byMember := make([][]spring, len(sets))
	local := make([]int, n)
	for _, m := range sets {
		for li, i := range m {
			local[i] = li
		}
	}
	for _, s := range coupling {
		k := at[find(s.a)]
		byMember[k] = append(byMember[k], spring{local[s.a], local[s.b], s.w})
	}

	x, y = make([]float64, n), make([]float64, n)
	cr := make([]float64, len(sets))
	for k, m := range sets {
		sr := make([]float64, len(m))
		for li, i := range m {
			sr[li] = r[i]
		}
		sx, sy := spread(sr, mean, maxW, byMember[k])
		cx, cy, R := enclose(sx, sy, sr)
		for li, i := range m {
			x[i], y[i] = sx[li]-cx, sy[li]-cy
		}
		cr[k] = R
	}
	if len(sets) > 1 {
		px, py := pack(cr, mean)
		for k, m := range sets {
			for _, i := range m {
				x[i] += px[k]
				y[i] += py[k]
			}
		}
	}
	compact(x, y, r, mean, coupling)
	return x, y
}

// pack places circles of radii r side by side without overlapping, largest
// first, each where it is nearest the first among the spots touching one
// already placed.
func pack(r []float64, mean float64) (x, y []float64) {
	n := len(r)
	x, y = make([]float64, n), make([]float64, n)
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(r[b], r[a]) })
	room := func(a, b int) float64 { return r[a] + r[b] + 0.5*gap*min(r[a], r[b], mean) }
	const turns = 24
	placed := []int{order[0]}
	for _, i := range order[1:] {
		bx, by, best := 0.0, 0.0, math.Inf(1)
		// Spots touching the circles placed last, which are the ones on
		// the edge; the inner ones are long surrounded.
		anchors := placed[max(0, len(placed)-64):]
		for _, a := range anchors {
			for t := range turns {
				sin, cos := math.Sincos(2 * math.Pi * float64(t) / turns)
				cx, cy := x[a]+room(a, i)*cos, y[a]+room(a, i)*sin
				d := math.Hypot(cx, cy)
				if d >= best {
					continue
				}
				free := true
				for _, o := range placed {
					if math.Hypot(cx-x[o], cy-y[o]) < room(o, i)-1e-9 {
						free = false
						break
					}
				}
				if free {
					bx, by, best = cx, cy, d
				}
			}
		}
		x[i], y[i] = bx, by
		placed = append(placed, i)
	}
	return x, y
}

// spread places circles of radii r, all coupled to one another directly or
// not, so that their distances follow the springs between them: by stress
// majorization from a fixed start and for a fixed number of steps, then
// pushing apart any still overlapping. maxW is the tightest coupling in the
// group.
func spread(r []float64, mean, maxW float64, coupling []spring) (x, y []float64) {
	n := len(r)
	x, y = make([]float64, n), make([]float64, n)
	if n == 1 {
		return x, y
	}
	// Gaps go by the smaller of two circles, so that a small package keeps
	// close to a large group rather than a gap the size of the average.
	unit := func(a, b int) float64 { return min(r[a], r[b], mean) }
	clear := func(a, b int) float64 { return r[a] + r[b] + gap*unit(a, b) }

	// The tightest pair is wanted just clear of each other, the loosest
	// a few bubbles further.
	adj := make([][]spring, n)
	for _, s := range coupling {
		s.w = clear(s.a, s.b) + 3*unit(s.a, s.b)*(1-s.w/maxW)
		adj[s.a] = append(adj[s.a], s)
		adj[s.b] = append(adj[s.b], spring{s.b, s.a, s.w})
	}
	d := distances(adj)
	for i := range n {
		for k := range n {
			if i != k {
				// A path can be shorter than the bubbles at its ends.
				d[i][k] = max(d[i][k], clear(i, k))
			}
		}
	}

	// Start on a sunflower spiral in tree order, so siblings start close.
	const golden = 2.399963229728653 // radians
	for i := range n {
		s := 2 * mean * math.Sqrt(float64(i)+0.5)
		x[i], y[i] = s*math.Cos(float64(i)*golden), s*math.Sin(float64(i)*golden)
	}

	// Each step moves every circle in turn to where its distances to all
	// the others, weighted by 1/d², are best met given where the others are
	// by then. Very large groups take fewer steps.
	steps := min(300, max(30, int(3e7/float64(n*n))))
	for range steps {
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
			x[i], y[i] = sx/sw, sy/sw
		}
	}

	// Distances are only met on average; push any circles still
	// overlapping apart.
	separate(x, y, r, mean, 500)
	return x, y
}

// separate pushes circles that overlap, or come closer than a sliver of
// room, apart, for at most rounds rounds or until none do. The smaller of
// two moves the more. It reports whether any still overlap.
func separate(x, y, r []float64, mean float64, rounds int) bool {
	n := len(r)
	for range rounds {
		moved := false
		for i := range n {
			for k := i + 1; k < n; k++ {
				dx, dy := x[k]-x[i], y[k]-y[i]
				want := r[i] + r[k] + 0.25*gap*min(r[i], r[k], mean)
				if math.Abs(dx) >= want || math.Abs(dy) >= want {
					continue
				}
				l := math.Hypot(dx, dy)
				if l >= want {
					continue
				}
				if l < 1e-9 {
					dx, dy, l = float64(k-i), 1, math.Hypot(float64(k-i), 1)
				}
				ai, ak := r[i]*r[i], r[k]*r[k]
				push := (want - l) / l
				x[i] -= dx * push * ak / (ai + ak)
				y[i] -= dy * push * ak / (ai + ak)
				x[k] += dx * push * ai / (ai + ak)
				y[k] += dy * push * ai / (ai + ak)
				moved = true
			}
		}
		if !moved {
			return false
		}
	}
	return true
}

// compact takes up the slack that the springs and the packing leave between
// circles: round by round, every circle takes a step towards the ones it is
// coupled to, or towards the middle of all if it is coupled to none, and
// those that then overlap are pushed apart again. The larger a circle, the
// smaller its step, so that small ones gather around a large one rather
// than drag it, and coupled ones stay side by side.
func compact(x, y, r []float64, mean float64, coupling []spring) {
	n := len(r)
	if n < 8 {
		// A few circles have little slack, and their distances say
		// more about their coupling.
		return
	}
	adj := make([][]spring, n)
	for _, s := range coupling {
		adj[s.a] = append(adj[s.a], s)
		adj[s.b] = append(adj[s.b], spring{s.b, s.a, s.w})
	}
	meanA := 0.0
	for i := range n {
		meanA += r[i] * r[i] / float64(n)
	}
	rounds := 40
	if n > 300 {
		rounds = 15
	}
	for range rounds {
		cx, cy, w := 0.0, 0.0, 0.0
		for i := range n {
			a := r[i] * r[i]
			cx, cy, w = cx+a*x[i], cy+a*y[i], w+a
		}
		cx, cy = cx/w, cy/w
		for i := range n {
			tx, ty := cx, cy
			if len(adj[i]) > 0 {
				tx, ty, w = 0, 0, 0
				for _, s := range adj[i] {
					tx, ty, w = tx+s.w*x[s.b], ty+s.w*y[s.b], w+s.w
				}
				tx, ty = tx/w, ty/w
			}
			step := 0.15 * meanA / (meanA + r[i]*r[i])
			x[i] += (tx - x[i]) * step
			y[i] += (ty - y[i]) * step
		}
		separate(x, y, r, mean, 20)
	}
	separate(x, y, r, mean, 1000)
}

// enclose returns a small circle around the circles at x, y of radii r: the
// farthest one is drawn in step by step, as Bădoiu and Clarkson do for
// points.
func enclose(x, y, r []float64) (cx, cy, R float64) {
	n := float64(len(r))
	for i := range r {
		cx += x[i] / n
		cy += y[i] / n
	}
	farthest := func() (int, float64) {
		f, fd := 0, -1.0
		for i := range r {
			if d := math.Hypot(x[i]-cx, y[i]-cy) + r[i]; d > fd {
				f, fd = i, d
			}
		}
		return f, fd
	}
	for t := 1; t <= 200; t++ {
		f, fd := farthest()
		l := fd - r[f]
		if l < 1e-12 {
			break
		}
		// The point of circle f farthest from the center.
		px, py := x[f]+(x[f]-cx)/l*r[f], y[f]+(y[f]-cy)/l*r[f]
		cx += (px - cx) / float64(t+1)
		cy += (py - cy) / float64(t+1)
	}
	_, R = farthest()
	return cx, cy, R
}

// spring links packages a and b with weight w: their coupling, or, once
// laid out, the length it asks for.
type spring struct {
	a, b int
	w    float64
}

// distances returns the shortest path lengths between all nodes of adj,
// +Inf where there is none, by Dijkstra from each.
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
			if it.d > dist[it.n] {
				continue
			}
			for _, s := range adj[it.n] {
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
	n int
	d float64
}

// queue is a min-heap of items by distance.
type queue []item

func (q queue) Len() int           { return len(q) }
func (q queue) Less(i, k int) bool { return q[i].d < q[k].d }
func (q queue) Swap(i, k int)      { q[i], q[k] = q[k], q[i] }
func (q *queue) Push(x any)        { *q = append(*q, x.(item)) }
func (q *queue) Pop() any {
	old := *q
	it := old[len(old)-1]
	*q = old[:len(old)-1]
	return it
}
