package review

import (
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
	var out []Hunk
	end := -1 // the last step shown so far
	for k := 0; k < len(changed); {
		lo := max(changed[k]-hunkContext, end+1)
		hi := min(changed[k]+hunkContext, len(shown)-1)
		for k++; k < len(changed) && changed[k]-hunkContext <= hi+1; k++ {
			hi = min(changed[k]+hunkContext, len(shown)-1)
		}
		h := Hunk{Skipped: lo - end - 1, Steps: shown[lo : hi+1]}
		depth := shown[lo].Depth
		for j := lo - 1; j >= 0 && depth > 0; j-- {
			if shown[j].Depth < depth {
				depth = shown[j].Depth
				h.Breadcrumb = append([]string{shown[j].Text}, h.Breadcrumb...)
			}
		}
		out = append(out, h)
		end = hi
	}
	if rest := len(shown) - 1 - end; rest > 0 {
		out = append(out, Hunk{Skipped: rest})
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
	olds, news := map[string]git.FileDiff{}, map[string]git.FileDiff{}
	for _, fd := range files {
		if fd.Old != nil {
			olds[fd.OldPath] = fd
		}
		if fd.New != nil {
			news[fd.NewPath] = fd
		}
	}
	flows := map[string]map[string][]ast.Step{}
	find := func(old bool, p, name string) *fn {
		fd, ok := news[p]
		f, src, side := fd.New, fd.NewSource, "new:"
		if old {
			fd, ok = olds[p]
			f, src, side = fd.Old, fd.OldSource, "old:"
		}
		if !ok || f == nil {
			return nil
		}
		for _, s := range f.Symbols {
			if (s.Kind == ast.KindFunc || s.Kind == ast.KindMethod) && s.Package+"."+s.QualifiedName() == name {
				fl, ok := flows[side+p]
				if !ok {
					fl = ast.Flows(p, src)
					flows[side+p] = fl
				}
				return &fn{sym: s, flow: fl[s.Key], dir: path.Dir(p)}
			}
		}
		return nil
	}
	isFunc := func(c semdiff.Change) bool {
		return (c.Symbol == "func" || c.Symbol == "method") &&
			!strings.HasSuffix(c.Path, "_test.go") && !strings.HasSuffix(c.OldPath, "_test.go")
	}

	added := map[string]*fn{} // by the report's name
	for _, c := range r.Changes {
		if c.Kind == semdiff.KindAdded && isFunc(c) {
			if f := find(false, c.Path, c.Name); f != nil {
				added[c.Name] = f
			}
		}
	}

	var out []Paths
	for i, c := range r.Changes {
		if !isFunc(c) {
			continue
		}
		if c.Kind == semdiff.KindAdded {
			if f := added[c.Name]; f != nil {
				out = append(out, outline(i, f.flow))
			}
			continue
		}
		if !c.Flags.Has(ast.BodyChanged) || c.Flags.Has(ast.Deleted) {
			continue
		}
		before := find(true, c.OldPath, cmpOr(c.OldName, c.Name))
		after := find(false, c.Path, c.Name)
		if before == nil || after == nil {
			continue
		}
		p := compare(before.flow, after.flow, callees(after, added))
		p.Change = i
		if p.Kind == PathsUnchanged {
			p.Starts, p.Stops = calls(before.sym, after.sym)
			p.Verdict = unchangedVerdict(p.Starts, p.Stops)
		}
		out = append(out, p)
	}
	return out
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
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
		for _, r := range f.sym.Refs {
			if r.Calls == 0 || r.Name != a.sym.Name {
				continue
			}
			ok := false
			switch r.Kind {
			case ast.RefLocal:
				ok = a.sym.Kind == ast.KindFunc && a.dir == f.dir
			case ast.RefMethod:
				ok = a.sym.Kind == ast.KindMethod && (r.Recv == "" || r.Recv == a.sym.Recv)
			case ast.RefImport:
				ok = a.sym.Kind == ast.KindFunc
			}
			if ok {
				shown := name
				if a.dir == f.dir && a.sym.Package == f.sym.Package {
					shown = a.sym.QualifiedName()
				}
				out = append(out, callee{name: shown, flow: a.flow})
				break
			}
		}
	}
	return out
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

// compare compares the flows of a function before and after an edit, with
// the functions the change adds that it calls after it, where steps it
// drops may have gone.
func compare(old, new []ast.Step, into []callee) Paths {
	var p Paths
	type op struct {
		mark StepMark
		i, j int // into old, new; -1 for none
	}
	// Align by key, longest common subsequence first.
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

	// Pair a removed step with an added step of the same run when they
	// share most of their words: one edited step, where the added one is.
	paired := map[int]int{} // new index to old index
	pairedOld := map[int]bool{}
	for start := 0; start < len(ops); {
		end := start
		for end < len(ops) && (ops[end].mark == StepRemoved || ops[end].mark == StepAdded) {
			end++
		}
		for _, d := range ops[start:end] {
			if d.mark != StepRemoved {
				continue
			}
			for _, a := range ops[start:end] {
				if a.mark == StepAdded && !hasKey(paired, a.j) && old[d.i].Kind == new[a.j].Kind && similar(old[d.i].Key, new[a.j].Key) {
					paired[a.j], pairedOld[d.i] = d.i, true
					break
				}
			}
		}
		start = max(end, start+1)
	}

	// Follow the removed steps into the functions the change adds that the
	// function calls: as they were first, then edited.
	used := make([]map[int]bool, len(into))
	for k := range used {
		used[k] = map[int]bool{}
	}
	moved := map[int]PathStep{} // by old index
	look := func(s ast.Step, exact bool) (PathStep, bool) {
		for k, c := range into {
			for x, t := range c.flow {
				if used[k][x] || t.Kind != s.Kind || exact && t.Key != s.Key || !exact && !similar(t.Key, s.Key) {
					continue
				}
				used[k][x] = true
				if exact {
					return PathStep{Step: s, Mark: StepMoved, To: c.name}, true
				}
				t.Depth = s.Depth
				return edit(PathStep{Step: t, Mark: StepMovedEdited, To: c.name}, s), true
			}
		}
		return PathStep{}, false
	}
	for _, exact := range []bool{true, false} {
		for _, o := range ops {
			if o.mark != StepRemoved || pairedOld[o.i] {
				continue
			}
			if _, done := moved[o.i]; done {
				continue
			}
			if ps, ok := look(old[o.i], exact); ok {
				moved[o.i] = ps
			}
		}
	}

	var added, removed, edited, renested, movedEdited int
	var newPaths, gonePaths, newEffects, goneEffects, newClosures, goneClosures int
	var onlyNew string
	into2 := map[string]bool{}
	for _, o := range ops {
		switch o.mark {
		case StepSame:
			p.Steps = append(p.Steps, PathStep{Step: new[o.j], Mark: StepSame})
		case StepRenested:
			renested++
			p.Steps = append(p.Steps, PathStep{Step: new[o.j], Mark: StepRenested})
		case StepAdded:
			if d, ok := paired[o.j]; ok {
				edited++
				p.Steps = append(p.Steps, edit(PathStep{Step: new[o.j], Mark: StepEdited}, old[d]))
				continue
			}
			added++
			switch new[o.j].Kind {
			case ast.StepDecision, ast.StepLoop:
				newPaths++
				onlyNew = new[o.j].String()
			case ast.StepClosure:
				newClosures++
			case ast.StepEffect:
				newEffects++
			case ast.StepExit, ast.StepJump:
			}
			p.Steps = append(p.Steps, PathStep{Step: new[o.j], Mark: StepAdded})
		case StepRemoved:
			if pairedOld[o.i] {
				continue
			}
			if ps, ok := moved[o.i]; ok {
				into2[ps.To] = true
				if ps.Mark == StepMoved {
					p.Moved++
				} else {
					movedEdited++
				}
				p.Steps = append(p.Steps, ps)
				continue
			}
			removed++
			switch old[o.i].Kind {
			case ast.StepDecision, ast.StepLoop:
				gonePaths++
			case ast.StepClosure:
				goneClosures++
			case ast.StepEffect:
				goneEffects++
			case ast.StepExit, ast.StepJump:
			}
			p.Steps = append(p.Steps, PathStep{Step: old[o.i], Mark: StepRemoved})
		case StepEdited, StepMoved, StepMovedEdited:
		}
	}
	for _, c := range into {
		if into2[c.name] {
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
	exitsChanged := 0
	for _, e := range p.Exits {
		if e.Mark != ExitSame {
			exitsChanged++
		}
	}
	if exitsChanged == 0 {
		p.Exits = nil
	}

	outMoved := p.Moved + movedEdited
	switch {
	case added+removed+edited+renested+outMoved == 0:
		p.Kind = PathsUnchanged
	case outMoved > 0 && 2*outMoved >= outMoved+removed:
		p.Kind = PathsExtracted
		v := "extracted into " + strings.Join(p.Into, ", ")
		switch {
		case movedEdited == 0 && removed == 0:
			v += " · all " + plural(p.Moved, "step") + " reappear"
		default:
			if movedEdited > 0 {
				v += " · " + plural(movedEdited, "step") + " moved and edited"
			}
			if removed > 0 {
				v += " · " + plural(removed, "step") + " don't reappear"
			}
		}
		p.Verdict = v
	case added+removed == 0:
		p.Kind = PathsReshaped
		parts := []string{"same paths"}
		if edited > 0 {
			parts = append(parts, plural(edited, "step")+" edited")
		}
		if renested > 0 {
			parts = append(parts, fmt.Sprintf("%d re-nested", renested))
		}
		p.Verdict = strings.Join(parts, " · ")
	default:
		p.Kind = PathsChanged
		var parts []string
		switch {
		case newPaths == 1 && added == 1:
			parts = append(parts, "1 new path: "+onlyNew)
		case newPaths > 0:
			parts = append(parts, plural(newPaths, "new path"))
		}
		if gonePaths > 0 {
			parts = append(parts, fmt.Sprintf("%d removed", gonePaths))
		}
		if exitsChanged > 0 {
			parts = append(parts, fmt.Sprintf("%s changed", waysOut(exitsChanged)))
		}
		if edited > 0 {
			parts = append(parts, plural(edited, "step")+" edited")
		}
		if renested > 0 {
			parts = append(parts, fmt.Sprintf("%d re-nested", renested))
		}
		if newEffects > 0 {
			parts = append(parts, plural(newEffects, "new effect"))
		}
		if goneEffects > 0 {
			parts = append(parts, plural(goneEffects, "effect")+" gone")
		}
		if newClosures > 0 {
			parts = append(parts, plural(newClosures, "new closure"))
		}
		if goneClosures > 0 {
			parts = append(parts, plural(goneClosures, "closure")+" gone")
		}
		if len(parts) == 0 {
			parts = append(parts, plural(added+removed, "step")+" changed")
		}
		p.Verdict = strings.Join(parts, " · ")
	}
	return p
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

// exitsOf lists the ways out of a flow with the decisions enclosing each,
// leaving out those inside closures and the steps whose index is in skip.
// An else or default is named after the conditions before it.
func exitsOf(flow []ast.Step, skip map[int]bool) []exitAt {
	var out []exitAt
	type frame struct {
		step      ast.Step
		when, key string
	}
	var stack []frame
	chains := map[int][]string{} // the conditions of the if or switch chain open at each depth
	for i, s := range flow {
		for len(stack) > 0 && stack[len(stack)-1].step.Depth >= s.Depth {
			stack = stack[:len(stack)-1]
		}
		inClosure := false
		for _, f := range stack {
			if f.step.Kind == ast.StepClosure {
				inClosure = true
			}
		}
		when := s.Cond
		switch {
		case s.Kind != ast.StepDecision:
			delete(chains, s.Depth)
		case strings.HasPrefix(s.Text, "if "):
			chains[s.Depth] = []string{s.Cond}
		case strings.HasPrefix(s.Text, "else if ") || strings.HasPrefix(s.Text, "case "):
			chains[s.Depth] = append(chains[s.Depth], s.Cond)
		case s.Text == "else" || s.Text == "default":
			switch c := chains[s.Depth]; len(c) {
			case 0:
				when = s.Text
			case 1:
				when = "not " + c[0]
			default:
				when = "none of " + strings.Join(c, "; ")
			}
		default: // switch, select
			delete(chains, s.Depth+1)
			when = s.Text
		}
		if s.Kind == ast.StepLoop {
			when = s.Text
		}
		whenKey := s.Key
		then, thenKey := s.Then, ""
		if then != "" {
			cut := strings.LastIndex(s.Key, " → ")
			whenKey, thenKey = s.Key[:cut], s.Key[cut+len(" → "):]
			if isJump(then) {
				then = ""
			}
		}
		if !inClosure && !skip[i] {
			var ws, ks []string
			for _, f := range stack {
				ws, ks = append(ws, f.when), append(ks, f.key)
			}
			switch {
			case s.Kind == ast.StepExit:
				out = append(out, exitAt{Exit{What: s.Text, When: ws, Line: s.Line}, s.Key, strings.Join(ks, " ∧ ")})
			case then != "":
				out = append(out, exitAt{Exit{What: then, When: append(ws, when), Line: s.Line}, thenKey, strings.Join(append(ks, whenKey), " ∧ ")})
			}
		}
		stack = append(stack, frame{s, when, whenKey})
	}
	return out
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
	for j, e := range new {
		for i, o := range old {
			if !matched[i] && o.what == e.what && o.when == e.when {
				matched[i], marks[j] = true, ExitSame
				break
			}
		}
	}
	// Then the same way out under other decisions, then one returning
	// something else under the same decisions.
	for _, same := range []func(o, e exitAt) bool{
		func(o, e exitAt) bool { return o.what == e.what },
		func(o, e exitAt) bool { return o.when == e.when && similar(o.what, e.what) },
	} {
		for j, e := range new {
			if marks[j] != 0 {
				continue
			}
			for i, o := range old {
				if !matched[i] && same(o, e) {
					matched[i], marks[j] = true, ExitChanged
					break
				}
			}
		}
	}
	for j := range marks {
		if marks[j] == 0 {
			marks[j] = ExitNew
		}
	}
	var out []Exit
	for j, e := range new {
		x := e.Exit
		x.Mark = marks[j]
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
