package review

import (
	"fmt"
	"strings"
	"testing"

	"huginn/internal/ast"
)

// flowOf returns the flow of the function keyed key in the Go source src,
// which needs no package clause.
func flowOf(t *testing.T, src, key string) []ast.Step {
	t.Helper()
	flows := ast.Flows("x.go", []byte("package x\n\n"+src))
	fl, ok := flows[key]
	if !ok {
		t.Fatalf("no function %s in\n%s", key, src)
	}
	return fl
}

// marked writes a compared flow one step a line: "mark text".
func marked(steps []PathStep) string {
	var b strings.Builder
	for _, s := range steps {
		fmt.Fprintf(&b, "%-11s %s%s", s.Mark, strings.Repeat("  ", s.Depth), s)
		if s.Old != "" {
			fmt.Fprintf(&b, " (was %s)", s.Old)
		}
		if s.To != "" {
			fmt.Fprintf(&b, " (in %s)", s.To)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func TestCompare(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		into          string // a function the change adds and the edited one calls
		kind          PathsKind
		verdict       string
		steps         string // the steps that are not the same, as marked writes them
		exits         []string
	}{
		{
			name: "a new guard",
			before: `type R struct{}
func (r *R) Target(base string, getenv func(string) string) (string, error) {
	if base != "" {
		return base, nil
	}
	if sha := getenv("SHA"); sha != "" {
		return sha, nil
	}
	b, err := r.def()
	if err != nil {
		return "", fmt.Errorf("name the branch with -base: %w", err)
	}
	return b, nil
}`,
			after: `type R struct{}
func (repo *R) Target(base string, getenv func(string) string) (string, error) {
	if base != "" {
		return base, nil
	}
	if getenv != nil {
		if sha := getenv("SHA"); sha != "" {
			return sha, nil
		}
	}
	b, err := repo.def()
	if err != nil {
		return "", fmt.Errorf("name the branch: %w", err)
	}
	return b, nil
}`,
			kind:    PathsChanged,
			verdict: "1 new path: if getenv != nil · 2 ways out changed · 1 step edited · 1 re-nested",
			steps: `added       if getenv != nil
renested      if sha := getenv("SHA"); sha != "" → return sha, nil
edited      if err != nil → return "", fmt.Errorf("name the branch: %w", err) (was if err != nil → return "", fmt.Errorf("name the branch with -base: %w", err))
`,
			exits: []string{
				`changed return sha, nil ⇐ getenv != nil ∧ sha := getenv("SHA"); sha != ""`,
				`changed return "", fmt.Errorf("name the branch: %w", err) ⇐ err != nil`,
			},
		},
		{
			name:    "straight-line edits and a renamed receiver",
			before:  "type T struct{ n int }\nfunc (t *T) Inc() int {\n\tt.n++\n\tif t.n > 9 {\n\t\treturn 9\n\t}\n\treturn t.n\n}",
			after:   "type T struct{ n int }\nfunc (x *T) Inc() int {\n\tx.n += 2\n\tif x.n > 9 {\n\t\treturn 9\n\t}\n\treturn x.n\n}",
			kind:    PathsUnchanged,
			verdict: "",
		},
		{
			name:    "a condition edited in place",
			before:  "func f(n int) int {\n\tif n > 0 {\n\t\treturn n\n\t}\n\treturn 0\n}",
			after:   "func f(n int) int {\n\tif n >= 0 {\n\t\treturn n\n\t}\n\treturn 0\n}",
			kind:    PathsReshaped,
			verdict: "same paths · 1 step edited",
			steps:   "edited      if n >= 0 → return n (was if n > 0 → return n)\n",
			exits:   []string{"changed return n ⇐ n >= 0"},
		},
		{
			name:    "a switch on a tag, with a new case",
			before:  "type T struct{ kind int }\nfunc (t T) rank() int {\n\tswitch {\n\tcase t.kind == 1:\n\t\treturn 1\n\t}\n\treturn 0\n}",
			after:   "type T struct{ kind int }\nfunc (t T) rank() int {\n\tswitch t.kind {\n\tcase 1:\n\t\treturn 1\n\tcase 2, 3:\n\t\treturn 3\n\t}\n\treturn 0\n}",
			kind:    PathsChanged,
			verdict: "1 new path: case 2, 3 → return 3 · 1 way out changed",
			steps:   "added         case 2, 3 → return 3\n",
			exits:   []string{"new return 3 ⇐ switch t.kind ∧ t.kind == 2 || t.kind == 3"},
		},
		{
			name: "an extraction",
			before: `func next(r *reader) (*commit, error) {
	c := &commit{}
	for {
		tok, err := r.token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("commit: %w", err)
		}
		if tok == "" {
			r.pending = true
			return c, nil
		}
	}
	return c, nil
}`,
			after: `func next(r *reader) (*commit, error) {
	c := &commit{}
	if err := files(r, c); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return c, nil
}`,
			into: `func files(r *reader, c *commit) error {
	for {
		tok, err := r.token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if tok == "" {
			r.pending = true
			return nil
		}
	}
}`,
			kind:    PathsExtracted,
			verdict: "extracted into files · 2 steps moved and edited",
			// The error wrapped once stays in next, an edit there rather
			// than a move.
			steps: `moved       for (in files)
moved         err ← r.token(…) (in files)
movedEdited   if err == io.EOF → return nil (was if err == io.EOF → break) (in files)
moved         if tok == "" (in files)
movedEdited     return nil (was return c, nil) (in files)
edited      if err := files(r, c); err != nil → return nil, fmt.Errorf("commit: %w", err) (was if err != nil → return nil, fmt.Errorf("commit: %w", err))
`,
			exits: []string{`changed return nil, fmt.Errorf("commit: %w", err) ⇐ err := files(r, c); err != nil`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := func(src string) string {
				for _, l := range strings.Split(src, "\n") {
					if rest, ok := strings.CutPrefix(l, "func "); ok {
						if strings.HasPrefix(rest, "(") {
							recv := rest[1:strings.Index(rest, ")")]
							typ := strings.TrimPrefix(recv[strings.LastIndex(recv, " ")+1:], "*")
							rest = rest[strings.Index(rest, ")")+2:]
							return typ + "." + rest[:strings.Index(rest, "(")]
						}
						return rest[:strings.Index(rest, "(")]
					}
				}
				return ""
			}
			var into []callee
			if tt.into != "" {
				into = append(into, callee{name: key(tt.into), flow: flowOf(t, tt.into, key(tt.into))})
			}
			p := compare(flowOf(t, tt.before, key(tt.before)), flowOf(t, tt.after, key(tt.after)), into)
			if p.Kind != tt.kind || tt.verdict != "" && p.Verdict != tt.verdict {
				t.Errorf("%s: %q, want %s: %q", p.Kind, p.Verdict, tt.kind, tt.verdict)
			}
			var changed []PathStep
			for _, s := range p.Steps {
				if s.Mark != StepSame {
					changed = append(changed, s)
				}
			}
			if got := marked(changed); got != tt.steps {
				t.Errorf("steps:\n%s\nwant:\n%s\nall:\n%s", got, tt.steps, marked(p.Steps))
			}
			var exits []string
			for _, e := range p.Exits {
				if e.Mark != ExitSame {
					exits = append(exits, fmt.Sprintf("%s %s ⇐ %s", e.Mark, e.What, strings.Join(e.When, " ∧ ")))
				}
			}
			if strings.Join(exits, "\n") != strings.Join(tt.exits, "\n") {
				t.Errorf("exits:\n%s\nwant:\n%s", strings.Join(exits, "\n"), strings.Join(tt.exits, "\n"))
			}
		})
	}
}

// An edited step whose difference lies far in is written from shortly
// before it on both sides, so that a report cutting it short still shows it.
func TestEdit(t *testing.T) {
	old := ast.Step{Text: "if err != nil", Then: `return "", errors.Join(err, errors.New("name the branch the merge request goes into with -base"))`}
	now := ast.Step{Text: "if err != nil", Then: `return "", errors.Join(err, errors.New("name the branch the merge request goes into"))`}
	ps := edit(PathStep{Step: now, Mark: StepEdited}, old)
	if ps.String() != `if err != nil → … the merge request goes into"))` || ps.Old != `if err != nil → … the merge request goes into with -base"))` {
		t.Errorf("edited: %q, was %q", ps.String(), ps.Old)
	}
	short := edit(PathStep{Step: ast.Step{Text: "if n >= 0"}, Mark: StepEdited}, ast.Step{Text: "if n > 0"})
	if short.String() != "if n >= 0" || short.Old != "if n > 0" {
		t.Errorf("a short step is kept whole: %q, was %q", short.String(), short.Old)
	}
}

func TestOutline(t *testing.T) {
	p := outline(3, flowOf(t, `func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	default:
		return 0
	}
}`, "sign"))
	if p.Change != 3 || p.Kind != PathsNew || p.Verdict != "2 decisions · 3 ways out" {
		t.Errorf("%+v", p)
	}
	if h := p.Hunks(); len(h) != 1 || len(h[0].Steps) != 4 {
		t.Errorf("hunks %+v", h)
	}
	ws := exitsOf(flowOf(t, "func f(a, b bool) int {\n\tif a {\n\t\treturn 1\n\t} else if b {\n\t\treturn 2\n\t} else {\n\t\treturn 3\n\t}\n}", "f"), nil)
	if len(ws) != 3 || ws[2].When[0] != "none of a; b" {
		t.Errorf("an else is named after the conditions before it: %+v", ws)
	}
}

func TestHunks(t *testing.T) {
	var src strings.Builder
	src.WriteString("func long(xs []int) {\n\tfor _, x := range xs {\n")
	for i := range 20 {
		fmt.Fprintf(&src, "\t\tif x == %d {\n\t\t\tcontinue\n\t\t}\n", i)
	}
	src.WriteString("\t}\n}")
	before := src.String()
	after := strings.Replace(before, "x == 14", "x == 41", 1)
	p := compare(flowOf(t, before, "long"), flowOf(t, after, "long"), nil)
	hs := p.Hunks()
	if len(hs) != 2 {
		t.Fatalf("%d hunks: %+v", len(hs), hs)
	}
	h := hs[0]
	if h.Skipped != 14 || len(h.Breadcrumb) != 1 || h.Breadcrumb[0] != "for _, x := range xs" || len(h.Steps) != 3 || h.Steps[1].Mark != StepEdited {
		t.Errorf("hunk: skipped %d, breadcrumb %q, steps:\n%s", h.Skipped, h.Breadcrumb, marked(h.Steps))
	}
	if hs[1].Skipped != 4 || hs[1].Steps != nil {
		t.Errorf("last hunk: %+v", hs[1])
	}

	same := compare(flowOf(t, before, "long"), flowOf(t, before, "long"), nil)
	if same.Kind != PathsUnchanged || same.Hunks() != nil {
		t.Errorf("no change: %s, %d hunks", same.Kind, len(same.Hunks()))
	}
}
