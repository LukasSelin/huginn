package review

import (
	"cmp"
	"fmt"
	gotypes "go/types"
	"maps"
	"path"
	"slices"
	"strings"

	"huginn/internal/ast"
	"huginn/internal/git"
	"huginn/internal/semdiff"
)

// PathsKind is the kind of verdict on what a change does to a function's
// paths.
type PathsKind uint8

const (
	// PathsNew is a function the change adds: its flow is an outline.
	PathsNew PathsKind = iota + 1

	// PathsUnchanged is an edit that leaves every step of the flow as it
	// was: what changed is straight-line code.
	PathsUnchanged

	// PathsExtracted is an edit that moves most of the steps it drops into
	// functions the change adds and the function now calls.
	PathsExtracted

	// PathsReshaped is an edit whose steps are all still there, some
	// edited or moved under another decision.
	PathsReshaped

	// PathsChanged is an edit that adds or removes steps.
	PathsChanged
)

func (k PathsKind) String() string {
	switch k {
	case PathsNew:
		return "new"
	case PathsUnchanged:
		return "unchanged"
	case PathsExtracted:
		return "extracted"
	case PathsReshaped:
		return "reshaped"
	case PathsChanged:
		return "changed"
	}
	return "unknown"
}

// MarshalText encodes a kind by its name.
func (k PathsKind) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

// StepMark says how a step of a compared flow differs from the other side.
type StepMark uint8

const (
	// StepSame is on both sides, at the same depth.
	StepSame StepMark = iota + 1

	// StepRenested is on both sides, under more or fewer decisions.
	StepRenested

	// StepEdited replaces a step of the other side that it shares most of
	// its words with; PathStep.Old is that step.
	StepEdited

	// StepAdded is only after the change.
	StepAdded

	// StepRemoved is only before the change, and is not found in a
	// function the change adds either.
	StepRemoved

	// StepMoved is a step the function no longer has, found as it was in
	// a function the change adds and the function calls, named by
	// PathStep.To.
	StepMoved

	// StepMovedEdited is a step the function no longer has, found edited in
	// a function the change adds and the function calls: PathStep.Step is
	// the step there, PathStep.Old the step as it was.
	StepMovedEdited
)

func (m StepMark) String() string {
	switch m {
	case StepSame:
		return "same"
	case StepRenested:
		return "renested"
	case StepEdited:
		return "edited"
	case StepAdded:
		return "added"
	case StepRemoved:
		return "removed"
	case StepMoved:
		return "moved"
	case StepMovedEdited:
		return "movedEdited"
	}
	return "unknown"
}

// MarshalText encodes a mark by its name.
func (m StepMark) MarshalText() ([]byte, error) { return []byte(m.String()), nil }

// ExitMark says how a way out of a function differs from the other side.
type ExitMark uint8

const (
	// ExitSame is reached as before.
	ExitSame ExitMark = iota + 1

	// ExitNew is a way out the function did not have.
	ExitNew

	// ExitGone is a way out the function no longer has.
	ExitGone

	// ExitChanged is a way out the function had, returning something else
	// or reached under other decisions.
	ExitChanged
)

func (m ExitMark) String() string {
	switch m {
	case ExitSame:
		return "same"
	case ExitNew:
		return "new"
	case ExitGone:
		return "gone"
	case ExitChanged:
		return "changed"
	}
	return "unknown"
}

// MarshalText encodes a mark by its name.
func (m ExitMark) MarshalText() ([]byte, error) { return []byte(m.String()), nil }

// Paths is what a change does to the paths through one function: its flows
// on either side compared, with a one-line verdict.
type Paths struct {
	Change  int // indexes Report.Changes
	Kind    PathsKind
	Verdict string // "3 new paths · 1 step edited"

	// Steps is the compared flow in order: the steps after the change, the
	// steps it removed where they were, and for a function the change
	// adds, its whole flow marked StepSame.
	Steps []PathStep

	// Exits are the ways out after the change, then those it lost, when
	// one of them differs; nil when none does, and for new functions.
	Exits []Exit

	// Decisions and WaysOut count the conditions the function tests and
	// the ways out of it, after the change, leaving out closures' ways out.
	Decisions, WaysOut int

	// Moved counts the steps that moved as they were into the functions in
	// Into, which an extraction moved steps into.
	Moved int
	Into  []string

	// Starts and Stops are the functions it starts and stops calling, for
	// an edit whose paths are unchanged.
	Starts, Stops []string
}

// PathStep is one step of a compared flow: the step, and how it differs
// from the other side.
type PathStep struct {
	ast.Step
	Mark StepMark
	Old  string // the step before the change, for StepEdited and StepMovedEdited
	To   string // the function a moved step went to
}

// Exit is one way out of a function, with the decisions enclosing it.
type Exit struct {
	What string   // "return nil, err"
	When []string // the decisions enclosing it, outermost first; empty at the top
	Line int
	Mark ExitMark
}

// Hunk is a run of a compared flow to show: changed steps with one step
// around them, after the unchanged steps it skips. A last Hunk without
// steps counts the unchanged steps after the last change.
type Hunk struct {
	Skipped    int
	Breadcrumb []string // the decisions enclosing its first step, outermost first, when not in it
	Steps      []PathStep
}

// A flow of at most wholeFlow steps is shown whole; a longer one is cut
// into hunks with hunkContext steps around each change.
const (
	wholeFlow   = 12
	hunkContext = 1
)

// Hunks returns what to show of the compared flow: the whole of a short or
// new one, the changed runs of a long one, and nothing when no step
// changed. Steps that moved as they were are left out: the verdict counts
// them.
func (p *Paths) Hunks() []Hunk {
	var shown []PathStep
	for _, s := range p.Steps {
		if s.Mark != StepMoved {
			shown = append(shown, s)
		}
	}
	if p.Kind == PathsNew {
		if len(shown) == 0 {
			return nil
		}
		return []Hunk{{Steps: shown}}
	}
	var changed []int
	for i, s := range shown {
		if s.Mark != StepSame {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	if len(shown) <= wholeFlow {
		return []Hunk{{Steps: shown}}
	}
	return cut(shown, changed)
}

// cut cuts a flow into hunks of the steps at the indexes in changed, with
// hunkContext steps around each, merging hunks that touch, and a last hunk
// counting the steps after them.
func cut(shown []PathStep, changed []int) []Hunk {
	var out []Hunk
	end := -1 // the last step shown so far
	for k := 0; k < len(changed); {
		lo := max(changed[k]-hunkContext, end+1)
		hi := min(changed[k]+hunkContext, len(shown)-1)
		for k++; k < len(changed) && changed[k]-hunkContext <= hi+1; k++ {
			hi = min(changed[k]+hunkContext, len(shown)-1)
		}
		out = append(out, Hunk{Skipped: lo - end - 1, Breadcrumb: breadcrumb(shown, lo), Steps: shown[lo : hi+1]})
		end = hi
	}
	if rest := len(shown) - 1 - end; rest > 0 {
		out = append(out, Hunk{Skipped: rest})
	}
	return out
}

// breadcrumb returns the steps enclosing shown[at], outermost first.
func breadcrumb(shown []PathStep, at int) []string {
	var out []string
	depth := shown[at].Depth
	for j := at - 1; j >= 0 && depth > 0; j-- {
		if shown[j].Depth < depth {
			depth = shown[j].Depth
			out = append([]string{shown[j].Text}, out...)
		}
	}
	return out
}

// PathsOf returns the paths of the change at index i of the report, or nil
// when it has none.
func (rv *Review) PathsOf(i int) *Paths {
	for k := range rv.Paths {
		if rv.Paths[k].Change == i {
			return &rv.Paths[k]
		}
	}
	return nil
}

// fn is a function on one side of the change, with its flow.
type fn struct {
	sym  *ast.Symbol
	flow []ast.Step
	dir  string
}

// paths compares the flows of the production functions the change adds or
// whose bodies it edits, in the order of the report's changes.
func paths(r *semdiff.Report, files []git.FileDiff) []Paths {
	sd := newSides(files)
	added := map[string]*fn{} // by the report's name
	for _, c := range r.Changes {
		if c.Kind == semdiff.KindAdded && isProductionFunc(c) {
			if f := sd.find(false, c.Path, c.Name); f != nil {
				added[c.Name] = f
			}
		}
	}

	var out []Paths
	for i, c := range r.Changes {
		switch {
		case !isProductionFunc(c):
		case c.Kind == semdiff.KindAdded:
			if f := added[c.Name]; f != nil {
				out = append(out, outline(i, f.flow))
			}
		case c.Flags.Has(ast.BodyChanged) && !c.Flags.Has(ast.Deleted):
			if p, ok := sd.edit(i, c, added); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// isProductionFunc reports whether a change is of a function or method
// outside test files.
func isProductionFunc(c semdiff.Change) bool {
	return (c.Symbol == "func" || c.Symbol == "method") &&
		!strings.HasSuffix(c.Path, "_test.go") && !strings.HasSuffix(c.OldPath, "_test.go")
}

// sides finds functions in the files a change touches, on either side,
// with their flows, reading each side of a file's flows once.
type sides struct {
	olds, news map[string]git.FileDiff
	flows      map[string]map[string][]ast.Step // by "old:" or "new:" and path
}

func newSides(files []git.FileDiff) *sides {
	sd := &sides{olds: map[string]git.FileDiff{}, news: map[string]git.FileDiff{}, flows: map[string]map[string][]ast.Step{}}
	for _, fd := range files {
		if fd.Old != nil {
			sd.olds[fd.OldPath] = fd
		}
		if fd.New != nil {
			sd.news[fd.NewPath] = fd
		}
	}
	return sd
}

// find returns the function or method named name, qualified with its
// package's name, in the file at path p before the change or after it, or
// nil when there is none.
func (sd *sides) find(old bool, p, name string) *fn {
	fd, ok := sd.news[p]
	f, src, side := fd.New, fd.NewSource, "new:"
	if old {
		fd, ok = sd.olds[p]
		f, src, side = fd.Old, fd.OldSource, "old:"
	}
	if !ok || f == nil {
		return nil
	}
	for _, s := range f.Symbols {
		if (s.Kind == ast.KindFunc || s.Kind == ast.KindMethod) && s.Package+"."+s.QualifiedName() == name {
			fl, ok := sd.flows[side+p]
			if !ok {
				fl = ast.Flows(p, src)
				sd.flows[side+p] = fl
			}
			return &fn{sym: s, flow: fl[s.Key], dir: path.Dir(p)}
		}
	}
	return nil
}

// edit compares the flows of the function the change at index i edits,
// following the steps it drops into the functions in added that it calls.
// It reports false when either side cannot be found.
func (sd *sides) edit(i int, c semdiff.Change, added map[string]*fn) (Paths, bool) {
	before := sd.find(true, c.OldPath, cmp.Or(c.OldName, c.Name))
	after := sd.find(false, c.Path, c.Name)
	if before == nil || after == nil {
		return Paths{}, false
	}
	p := compare(before.flow, after.flow, callees(after, added))
	p.Change = i
	if p.Kind == PathsUnchanged {
		p.Starts, p.Stops = calls(before.sym, after.sym)
		p.Verdict = unchangedVerdict(p.Starts, p.Stops)
	}
	return p, true
}

// callee is a function the change adds that an edited function calls, by
// the name the verdict gives it.
type callee struct {
	name string
	flow []ast.Step
}

// callees returns the functions in added that f calls, matched by name as
// the rest of huginn matches calls: a function of the same directory for
// an unqualified call, a method of the receiver's type or of any type for
// a method call, a function of any package for a qualified one.
func callees(f *fn, added map[string]*fn) []callee {
	var out []callee
	for _, name := range slices.Sorted(maps.Keys(added)) {
		a := added[name]
		if !slices.ContainsFunc(f.sym.Refs, func(r ast.Ref) bool { return callsTo(r, f, a) }) {
			continue
		}
		shown := name
		if a.dir == f.dir && a.sym.Package == f.sym.Package {
			shown = a.sym.QualifiedName()
		}
		out = append(out, callee{name: shown, flow: a.flow})
	}
	return out
}

// callsTo reports whether the reference r in f is a call to a, by name.
func callsTo(r ast.Ref, f, a *fn) bool {
	if r.Calls == 0 || r.Name != a.sym.Name {
		return false
	}
	switch r.Kind {
	case ast.RefLocal:
		return a.sym.Kind == ast.KindFunc && a.dir == f.dir
	case ast.RefMethod:
		return a.sym.Kind == ast.KindMethod && (r.Recv == "" || r.Recv == a.sym.Recv)
	case ast.RefImport:
		return a.sym.Kind == ast.KindFunc
	}
	return false
}

// outline is the paths of a function the change adds.
func outline(change int, flow []ast.Step) Paths {
	p := Paths{Change: change, Kind: PathsNew}
	for _, s := range flow {
		p.Steps = append(p.Steps, PathStep{Step: s, Mark: StepSame})
	}
	p.Decisions, p.WaysOut = counts(flow)
	p.Verdict = plural(p.Decisions, "decision")
	if p.Decisions == 0 {
		p.Verdict = "no decisions"
	}
	// A function that only runs off its end has one way out, which is no
	// news.
	if p.WaysOut > 0 {
		p.Verdict += " · " + waysOut(p.WaysOut)
	}
	return p
}

// counts returns how many conditions a flow tests and how many ways out of
// the function it has.
func counts(flow []ast.Step) (decisions, exits int) {
	for _, s := range flow {
		if s.Kind == ast.StepDecision && s.Cond != "" {
			decisions++
		}
	}
	return decisions, len(exitsOf(flow, nil))
}

func waysOut(n int) string {
	if n == 1 {
		return "1 way out"
	}
	return fmt.Sprintf("%d ways out", n)
}

// op is one step of the alignment of two flows: a step on both sides
// (StepSame or StepRenested), only before (StepRemoved) or only after
// (StepAdded), by its index on each side, -1 on a side it is not on.
type op struct {
	mark StepMark
	i, j int
}

// compare compares the flows of a function before and after an edit, with
// the functions the change adds that it calls after it, where steps it
// drops may have gone.
func compare(old, new []ast.Step, into []callee) Paths {
	ops := align(old, new)
	paired, pairedOld := pairEdits(ops, old, new)
	moved := follow(ops, old, pairedOld, into)

	var p Paths
	t := p.lay(ops, old, new, paired, pairedOld, moved)
	for _, c := range into {
		if t.into[c.name] {
			p.Into = append(p.Into, c.name)
		}
	}
	p.Decisions, p.WaysOut = counts(new)

	// The ways out, leaving out those of steps that moved to another
	// function: the steps say where they went.
	gone := map[int]bool{}
	for k := range moved {
		gone[k] = true
	}
	p.Exits = compareExits(exitsOf(old, gone), exitsOf(new, nil))
	for _, e := range p.Exits {
		if e.Mark != ExitSame {
			t.exits++
		}
	}
	if t.exits == 0 {
		p.Exits = nil
	}
	p.Kind, p.Verdict = t.verdict(p.Into)
	return p
}

// align aligns two flows by their steps' keys, longest common subsequence
// first. A step on both sides at different depths is re-nested.
func align(old, new []ast.Step) []op {
	n, m := len(old), len(new)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if old[i].Key == new[j].Key {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var ops []op
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && old[i].Key == new[j].Key:
			mark := StepSame
			if old[i].Depth != new[j].Depth {
				mark = StepRenested
			}
			ops = append(ops, op{mark, i, j})
			i++
			j++
		case j == m || i < n && lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, op{StepRemoved, i, -1})
			i++
		default:
			ops = append(ops, op{StepAdded, -1, j})
			j++
		}
	}
	return ops
}

// pairEdits pairs a removed step with an added step of the same run of
// changes when they share most of their words: one edited step, shown
// where the added one is. It returns the pairs by the added step's index,
// and the removed steps paired.
func pairEdits(ops []op, old, new []ast.Step) (paired map[int]int, pairedOld map[int]bool) {
	paired, pairedOld = map[int]int{}, map[int]bool{}
	for start := 0; start < len(ops); {
		end := start
		for end < len(ops) && (ops[end].mark == StepRemoved || ops[end].mark == StepAdded) {
			end++
		}
		for _, d := range ops[start:end] {
			if d.mark != StepRemoved {
				continue
			}
			if a, ok := editOf(ops[start:end], old[d.i], new, paired); ok {
				paired[a], pairedOld[d.i] = d.i, true
			}
		}
		start = max(end, start+1)
	}
	return paired, pairedOld
}

// editOf returns the index of the first added step of run, not yet
// paired, that is an edit of the removed step s.
func editOf(run []op, s ast.Step, new []ast.Step, paired map[int]int) (int, bool) {
	for _, a := range run {
		if a.mark == StepAdded && !hasKey(paired, a.j) && s.Kind == new[a.j].Kind && similar(s.Key, new[a.j].Key) {
			return a.j, true
		}
	}
	return 0, false
}

// follow looks for the removed steps that are not edits in the functions
// the change adds and the function calls: as they were first, then
// edited. It returns where each went, by its index before the change.
func follow(ops []op, old []ast.Step, pairedOld map[int]bool, into []callee) map[int]PathStep {
	moved := map[int]PathStep{}
	f := finder{into: into, used: make([]map[int]bool, len(into))}
	for k := range f.used {
		f.used[k] = map[int]bool{}
	}
	for _, exact := range []bool{true, false} {
		for _, o := range ops {
			if _, done := moved[o.i]; done || o.mark != StepRemoved || pairedOld[o.i] {
				continue
			}
			if ps, ok := f.look(old[o.i], exact); ok {
				moved[o.i] = ps
			}
		}
	}
	return moved
}

// finder looks for steps in the functions an edited function calls, each
// of their steps found once.
type finder struct {
	into []callee
	used []map[int]bool // by function, the indexes of the steps found
}

// look finds the step s in the functions, as it was or edited.
func (f *finder) look(s ast.Step, exact bool) (PathStep, bool) {
	for k, c := range f.into {
		for x, t := range c.flow {
			if f.used[k][x] || !matches(s, t, exact) {
				continue
			}
			f.used[k][x] = true
			if exact {
				return PathStep{Step: s, Mark: StepMoved, To: c.name}, true
			}
			t.Depth = s.Depth
			return edit(PathStep{Step: t, Mark: StepMovedEdited, To: c.name}, s), true
		}
	}
	return PathStep{}, false
}

// matches reports whether t is the step s, as it was or, when not exact,
// edited.
func matches(s, t ast.Step, exact bool) bool {
	switch {
	case t.Kind != s.Kind:
		return false
	case exact:
		return t.Key == s.Key
	}
	return similar(t.Key, s.Key)
}

// tally counts how two flows differ, for the verdict.
type tally struct {
	added, removed, edited, renested, moved, movedEdited int

	newPaths, gonePaths, newEffects, goneEffects, newClosures, goneClosures int

	exits   int             // ways out that differ
	onlyNew string          // the last new path, named when it is the only change
	into    map[string]bool // the functions steps moved to
}

// lay lays the compared flow out in p.Steps, in order, and tallies how it
// differs.
func (p *Paths) lay(ops []op, old, new []ast.Step, paired map[int]int, pairedOld map[int]bool, moved map[int]PathStep) *tally {
	t := &tally{into: map[string]bool{}}
	for _, o := range ops {
		switch o.mark {
		case StepSame:
			p.Steps = append(p.Steps, PathStep{Step: new[o.j], Mark: StepSame})
		case StepRenested:
			t.renested++
			p.Steps = append(p.Steps, PathStep{Step: new[o.j], Mark: StepRenested})
		case StepAdded:
			p.Steps = append(p.Steps, t.addedStep(o, old, new, paired))
		case StepRemoved:
			if ps, ok := t.removedStep(o, old, pairedOld, moved); ok {
				p.Steps = append(p.Steps, ps)
			}
		case StepEdited, StepMoved, StepMovedEdited:
		}
	}
	p.Moved = t.moved
	return t
}

// addedStep is the step added after the change, or the edit of the removed
// step paired with it.
func (t *tally) addedStep(o op, old, new []ast.Step, paired map[int]int) PathStep {
	s := new[o.j]
	if d, ok := paired[o.j]; ok {
		t.edited++
		return edit(PathStep{Step: s, Mark: StepEdited}, old[d])
	}
	t.added++
	switch s.Kind {
	case ast.StepDecision, ast.StepLoop:
		t.newPaths++
		t.onlyNew = s.String()
	case ast.StepClosure:
		t.newClosures++
	case ast.StepEffect:
		t.newEffects++
	case ast.StepExit, ast.StepJump:
	}
	return PathStep{Step: s, Mark: StepAdded}
}

// removedStep is the step removed by the change, or where it moved to. It
// reports false for one paired with an added step, which shows it.
func (t *tally) removedStep(o op, old []ast.Step, pairedOld map[int]bool, moved map[int]PathStep) (PathStep, bool) {
	if pairedOld[o.i] {
		return PathStep{}, false
	}
	if ps, ok := moved[o.i]; ok {
		t.into[ps.To] = true
		if ps.Mark == StepMoved {
			t.moved++
		} else {
			t.movedEdited++
		}
		return ps, true
	}
	t.removed++
	s := old[o.i]
	switch s.Kind {
	case ast.StepDecision, ast.StepLoop:
		t.gonePaths++
	case ast.StepClosure:
		t.goneClosures++
	case ast.StepEffect:
		t.goneEffects++
	case ast.StepExit, ast.StepJump:
	}
	return PathStep{Step: s, Mark: StepRemoved}, true
}

// verdict returns the kind of change the tally adds up to and the line
// saying what it is; the line of PathsUnchanged is left to the caller,
// which knows the calls.
func (t *tally) verdict(into []string) (PathsKind, string) {
	out := t.moved + t.movedEdited
	switch {
	case t.added+t.removed+t.edited+t.renested+out == 0:
		return PathsUnchanged, ""
	case out > 0 && 2*out >= out+t.removed:
		return PathsExtracted, t.extracted(into)
	case t.added+t.removed == 0:
		return PathsReshaped, strings.Join(append([]string{"same paths"}, t.edits()...), " · ")
	}
	return PathsChanged, t.changed()
}

// extracted says where an extraction moved the steps, and how many were
// edited on the way or do not reappear.
func (t *tally) extracted(into []string) string {
	v := "extracted into " + strings.Join(into, ", ")
	if t.movedEdited == 0 && t.removed == 0 {
		return v + " · all " + plural(t.moved, "step") + " reappear"
	}
	if t.movedEdited > 0 {
		v += " · " + plural(t.movedEdited, "step") + " moved and edited"
	}
	if t.removed > 0 {
		v += " · " + plural(t.removed, "step") + " don't reappear"
	}
	return v
}

// edits counts the steps edited and re-nested: "2 steps edited",
// "1 re-nested".
func (t *tally) edits() []string {
	var parts []string
	if t.edited > 0 {
		parts = append(parts, plural(t.edited, "step")+" edited")
	}
	if t.renested > 0 {
		parts = append(parts, fmt.Sprintf("%d re-nested", t.renested))
	}
	return parts
}

// changed counts what an edit adds and removes: new and removed paths,
// ways out changed, steps edited and re-nested, effects and closures.
func (t *tally) changed() string {
	var parts []string
	switch {
	case t.newPaths == 1 && t.added == 1:
		parts = append(parts, "1 new path: "+t.onlyNew)
	case t.newPaths > 0:
		parts = append(parts, plural(t.newPaths, "new path"))
	}
	if t.gonePaths > 0 {
		parts = append(parts, fmt.Sprintf("%d removed", t.gonePaths))
	}
	if t.exits > 0 {
		parts = append(parts, waysOut(t.exits)+" changed")
	}
	parts = append(parts, t.edits()...)
	for _, c := range []struct {
		n    int
		text string
	}{
		{t.newEffects, plural(t.newEffects, "new effect")},
		{t.goneEffects, plural(t.goneEffects, "effect") + " gone"},
		{t.newClosures, plural(t.newClosures, "new closure")},
		{t.goneClosures, plural(t.goneClosures, "closure") + " gone"},
	} {
		if c.n > 0 {
			parts = append(parts, c.text)
		}
	}
	if len(parts) == 0 {
		parts = append(parts, plural(t.added+t.removed, "step")+" changed")
	}
	return strings.Join(parts, " · ")
}

// edit gives an edited step its text before the change, both written from
// shortly before where they first differ when that is far in, so that the
// difference shows once a report shortens them.
func edit(ps PathStep, old ast.Step) PathStep {
	a, b := []rune(old.String()), []rune(ps.String())
	same := 0
	for same < len(a) && same < len(b) && a[same] == b[same] {
		same++
	}
	ps.Old = string(a)
	if same > editVisible {
		// Cut after a space, so that neither side starts mid-word.
		head := editHead
		for head > editHead/2 && a[head-1] != ' ' {
			head--
		}
		cut := same - editLead
		for k := cut; k > cut-editLead/2; k-- {
			if a[k-1] == ' ' {
				cut = k
				break
			}
		}
		ps.Old = string(a[:head]) + "… " + string(a[cut:])
		ps.Text, ps.Then = string(b[:head])+"… "+string(b[cut:]), ""
	}
	return ps
}

// An edited step whose first difference is past editVisible characters
// keeps its first editHead characters, then editLead characters before the
// difference.
const (
	editVisible = 72
	editHead    = 16
	editLead    = 24
)

func hasKey(m map[int]int, k int) bool {
	_, ok := m[k]
	return ok
}

// similar reports whether two keys share at least half their words, as a
// condition edited in place or an error wrapped differently does.
func similar(a, b string) bool {
	ta, tb := strings.Fields(a), strings.Fields(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	count := map[string]int{}
	for _, t := range ta {
		count[t]++
	}
	same := 0
	for _, t := range tb {
		if count[t] > 0 {
			count[t]--
			same++
		}
	}
	return 2*same >= max(len(ta), len(tb))
}

// exitAt is a way out of a flow, keyed by what it returns and the keys of
// the decisions enclosing it.
type exitAt struct {
	Exit
	what, when string
}

// exitFrame is a step enclosing others while a flow's ways out are listed,
// with the condition it stands for, as shown and as keyed.
type exitFrame struct {
	step      ast.Step
	when, key string
}

// exitsOf lists the ways out of a flow with the decisions enclosing each,
// leaving out those inside closures and the steps whose index is in skip.
// An else or default is named after the conditions before it.
func exitsOf(flow []ast.Step, skip map[int]bool) []exitAt {
	var out []exitAt
	var stack []exitFrame
	chains := map[int][]string{} // the conditions of the if or switch chain open at each depth
	for i, s := range flow {
		for len(stack) > 0 && stack[len(stack)-1].step.Depth >= s.Depth {
			stack = stack[:len(stack)-1]
		}
		whenKey, then, thenKey := splitThen(s)
		self := exitFrame{s, condition(s, chains), whenKey}
		if e, ok := exitOf(self, stack, then, thenKey); ok && !skip[i] && !inClosure(stack) {
			out = append(out, e)
		}
		stack = append(stack, self)
	}
	return out
}

// condition returns what a step stands for when it encloses a way out: a
// decision's condition, an else or default named after the conditions
// before it, a loop, switch or select as written. It keeps in chains the
// conditions of the if or switch chain open at each depth.
func condition(s ast.Step, chains map[int][]string) string {
	switch {
	case s.Kind == ast.StepLoop:
		delete(chains, s.Depth)
		return s.Text
	case s.Kind != ast.StepDecision:
		delete(chains, s.Depth)
	case strings.HasPrefix(s.Text, "if "):
		chains[s.Depth] = []string{s.Cond}
	case strings.HasPrefix(s.Text, "else if ") || strings.HasPrefix(s.Text, "case "):
		chains[s.Depth] = append(chains[s.Depth], s.Cond)
	case s.Text == "else" || s.Text == "default":
		return otherwise(s.Text, chains[s.Depth])
	default: // switch, select
		delete(chains, s.Depth+1)
		return s.Text
	}
	return s.Cond
}

// otherwise names an else or default after the conditions before it.
func otherwise(text string, conds []string) string {
	switch len(conds) {
	case 0:
		return text
	case 1:
		return "not " + conds[0]
	}
	return "none of " + strings.Join(conds, "; ")
}

// splitThen splits the key of a decision holding a way out or jump into
// the decision's and the way out's, and returns the way out it holds,
// empty for a jump or none.
func splitThen(s ast.Step) (whenKey, then, thenKey string) {
	cut := strings.LastIndex(s.Key, " → ")
	if s.Then == "" || cut < 0 {
		return s.Key, "", ""
	}
	whenKey, thenKey = s.Key[:cut], s.Key[cut+len(" → "):]
	if isJump(s.Then) {
		return whenKey, "", thenKey
	}
	return whenKey, s.Then, thenKey
}

// exitOf returns the way out self's step is, if it is one, under the
// decisions on the stack: an exit, or a decision holding one.
func exitOf(self exitFrame, stack []exitFrame, then, thenKey string) (exitAt, bool) {
	var ws, ks []string
	for _, f := range stack {
		ws, ks = append(ws, f.when), append(ks, f.key)
	}
	s := self.step
	switch {
	case s.Kind == ast.StepExit:
		return exitAt{Exit{What: s.Text, When: ws, Line: s.Line}, s.Key, strings.Join(ks, " ∧ ")}, true
	case then != "":
		return exitAt{Exit{What: then, When: append(ws, self.when), Line: s.Line}, thenKey, strings.Join(append(ks, self.key), " ∧ ")}, true
	}
	return exitAt{}, false
}

// inClosure reports whether a closure encloses the steps under the stack.
func inClosure(stack []exitFrame) bool {
	return slices.ContainsFunc(stack, func(f exitFrame) bool { return f.step.Kind == ast.StepClosure })
}

func isJump(s string) bool {
	for _, j := range []string{"break", "continue", "goto", "fallthrough"} {
		if s == j || strings.HasPrefix(s, j+" ") {
			return true
		}
	}
	return false
}

// compareExits marks the ways out after a change against those before it:
// the same where both what and when agree, changed where only one of them
// does, new and gone for the rest. Gone ones come last.
func compareExits(old, new []exitAt) []Exit {
	matched := make([]bool, len(old))
	marks := make([]ExitMark, len(new))
	// The same way out under the same decisions first, then under other
	// decisions, then one returning something else under the same ones.
	matchExits(old, new, matched, marks, ExitSame, func(o, e exitAt) bool { return o.what == e.what && o.when == e.when })
	matchExits(old, new, matched, marks, ExitChanged, func(o, e exitAt) bool { return o.what == e.what })
	matchExits(old, new, matched, marks, ExitChanged, func(o, e exitAt) bool { return o.when == e.when && similar(o.what, e.what) })
	var out []Exit
	for j, e := range new {
		x := e.Exit
		x.Mark = cmp.Or(marks[j], ExitNew)
		out = append(out, x)
	}
	for i, o := range old {
		if !matched[i] {
			x := o.Exit
			x.Mark = ExitGone
			out = append(out, x)
		}
	}
	return out
}

// matchExits marks as mark each way out in new not marked yet that is the
// same as one in old not matched yet.
func matchExits(old, new []exitAt, matched []bool, marks []ExitMark, mark ExitMark, same func(o, e exitAt) bool) {
	for j, e := range new {
		if marks[j] != 0 {
			continue
		}
		for i, o := range old {
			if !matched[i] && same(o, e) {
				matched[i], marks[j] = true, mark
				break
			}
		}
	}
}

// calls returns the functions after calls and before does not, and the
// other way round, by the names the references give them.
func calls(before, after *ast.Symbol) (starts, stops []string) {
	names := func(s *ast.Symbol) map[string]bool {
		out := map[string]bool{}
		for _, r := range s.Refs {
			if r.Calls == 0 {
				continue
			}
			switch r.Kind {
			case ast.RefLocal:
				if gotypes.Universe.Lookup(r.Name) == nil {
					out[r.Name] = true
				}
			case ast.RefImport:
				out[ast.ImportName(r.Pkg)+"."+r.Name] = true
			case ast.RefMethod:
				out[r.Recv+"."+r.Name] = true // ".Name" when only the name is known
			}
		}
		return out
	}
	b, a := names(before), names(after)
	for n := range a {
		if !b[n] {
			starts = append(starts, n)
		}
	}
	for n := range b {
		if !a[n] {
			stops = append(stops, n)
		}
	}
	slices.Sort(starts)
	slices.Sort(stops)
	return starts, stops
}

// unchangedVerdict says that an edit leaves the paths as they were, with
// the calls it starts and stops making.
func unchangedVerdict(starts, stops []string) string {
	v := "paths unchanged"
	if len(starts) > 0 {
		v += " · now calls " + someOf(starts)
	}
	if len(stops) > 0 {
		v += " · no longer calls " + someOf(stops)
	}
	return v
}

// someOf names up to three of names: "a, b, c and 2 more".
func someOf(names []string) string {
	if len(names) <= 3 {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:3], ", ") + fmt.Sprintf(" and %d more", len(names)-3)
}
