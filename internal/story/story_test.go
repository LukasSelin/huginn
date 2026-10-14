package story

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"huginn/internal/ast"
	"huginn/internal/codemap"
	"huginn/internal/journey"
	"huginn/internal/lineage"
)

// synth writes a synthetic history commit by commit.
type synth struct {
	h    *lineage.History
	t    time.Time
	n    int
	live map[string]*ast.Symbol // by dir/name
}

func newSynth() *synth {
	return &synth{h: lineage.New(), t: time.Date(2025, 1, 6, 9, 0, 0, 0, time.UTC), live: map[string]*ast.Symbol{}}
}

func fn(dir, name string, exported bool) *ast.Symbol {
	return &ast.Symbol{Kind: ast.KindFunc, Name: name, Key: name, Path: dir + "/x.go", Exported: exported, Signature: "func " + name + "()", StartLine: 1, EndLine: 4}
}

// commit applies one revision after gap, made of the given changes.
func (s *synth) commit(gap time.Duration, changes ...ast.Change) {
	s.t = s.t.Add(gap)
	s.n++
	s.h.Apply(lineage.Revision{SHA: fmt.Sprintf("c%03d", s.n), Time: s.t, Author: "ann", Changes: changes})
}

func (s *synth) add(dir, name string) ast.Change {
	n := fn(dir, name, true)
	s.live[dir+"/"+name] = n
	return ast.Change{Flags: ast.Added, New: n}
}

func (s *synth) edit(dir, name string) ast.Change {
	o := s.live[dir+"/"+name]
	n := *o
	n.Stmts++
	s.live[dir+"/"+name] = &n
	return ast.Change{Flags: ast.BodyChanged, Old: o, New: &n}
}

func (s *synth) del(dir, name string) ast.Change {
	o := s.live[dir+"/"+name]
	delete(s.live, dir+"/"+name)
	return ast.Change{Flags: ast.Deleted, Old: o}
}

func (s *synth) rename(dir, from, to string) ast.Change {
	o := s.live[dir+"/"+from]
	n := *o
	n.Name, n.Key = to, to
	delete(s.live, dir+"/"+from)
	s.live[dir+"/"+to] = &n
	return ast.Change{Flags: ast.Renamed, Old: o, New: &n}
}

func (s *synth) move(from, name, to string) ast.Change {
	o := s.live[from+"/"+name]
	n := *o
	n.Path = to + "/x.go"
	delete(s.live, from+"/"+name)
	s.live[to+"/"+name] = &n
	return ast.Change{Flags: ast.Moved, Old: o, New: &n}
}

func (s *synth) resign(dir, name string) ast.Change {
	o := s.live[dir+"/"+name]
	n := *o
	n.Signature += " error"
	s.live[dir+"/"+name] = &n
	return ast.Change{Flags: ast.SignatureChanged, Old: o, New: &n}
}

func (s *synth) story(t *testing.T) *Story {
	t.Helper()
	m := codemap.Build(s.h)
	j := journey.Build(s.h, m, journey.Commit, journey.Options{})
	st, err := Build(context.Background(), s.h, j, Templates{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// threeActs is a history in three clear acts, a day between commits:
//
//	commits 1-12:  core is written and edited
//	commits 13-24: api is born and most work moves there
//	                 (commit 20 renames three api functions)
//	a 30-day pause
//	commits 25-36: store is born, five core functions move into it, and
//	               api's exported signatures change; core loses its last
//	               symbol and dies
func threeActs() *synth {
	s := newSynth()
	const day = 24 * time.Hour
	s.commit(day, s.add("core", "A"), s.add("core", "B"), s.add("core", "C"), s.add("core", "D"), s.add("core", "E"), s.add("core", "F"))
	for i := 2; i <= 12; i++ {
		s.commit(day, s.edit("core", "A"), s.edit("core", "B"))
	}
	s.commit(day, s.add("api", "Get"), s.add("api", "Put"), s.add("api", "List"), s.add("api", "Del"))
	for i := 14; i <= 24; i++ {
		if i == 20 {
			s.commit(day, s.rename("api", "Get", "Fetch"), s.rename("api", "Put", "Store"), s.rename("api", "Del", "Remove"))
			continue
		}
		s.commit(day, s.edit("api", "List"))
	}
	s.commit(30*day, s.add("store", "Open"), s.move("core", "A", "store"), s.move("core", "B", "store"), s.move("core", "C", "store"), s.move("core", "D", "store"), s.move("core", "E", "store"))
	s.commit(day, s.resign("api", "Fetch"), s.resign("api", "Store"))
	for i := 27; i <= 35; i++ {
		s.commit(day, s.edit("store", "Open"), s.edit("store", "A"))
	}
	s.commit(day, s.del("core", "F"))
	return s
}

func TestThreeActs(t *testing.T) {
	st := threeActs().story(t)
	if len(st.Chapters) != 3 {
		for _, c := range st.Chapters {
			t.Logf("%d-%d %s", c.FirstRev, c.LastRev, c.Title)
		}
		t.Fatalf("%d chapters, want 3", len(st.Chapters))
	}
	want := []struct {
		first, last int
		hottest     string
		births      []string
	}{
		{0, 11, "core", []string{"core"}},
		{12, 23, "api", []string{"api"}},
		{24, 35, "store", []string{"store"}},
	}
	for i, w := range want {
		c := st.Chapters[i]
		if c.FirstRev != w.first || c.LastRev != w.last || c.Hottest != w.hottest || !reflect.DeepEqual(c.Births, w.births) {
			t.Errorf("chapter %d: revs %d-%d hottest %q births %v; want %d-%d %q %v",
				i+1, c.FirstRev, c.LastRev, c.Hottest, c.Births, w.first, w.last, w.hottest, w.births)
		}
		if c.Number != i+1 || c.Of != 3 || c.Commits != w.last-w.first+1 {
			t.Errorf("chapter %d: number %d of %d, %d commits", i+1, c.Number, c.Of, c.Commits)
		}
	}
	c3 := st.Chapters[2]
	if !reflect.DeepEqual(c3.Deaths, []string{"core"}) || c3.Added != 1 || c3.Removed != 1 {
		t.Errorf("chapter 3: deaths %v, added %d, removed %d", c3.Deaths, c3.Added, c3.Removed)
	}
	if st.Chapters[0].Title != "Beginnings: core" || st.Chapters[1].Title != "Enter api" || st.Chapters[2].Title != "Enter store" {
		t.Errorf("titles %q, %q, %q", st.Chapters[0].Title, st.Chapters[1].Title, st.Chapters[2].Title)
	}

	kinds := func(c Chapter) []string {
		var out []string
		for _, m := range c.Moments {
			out = append(out, m.Kind.String())
		}
		return out
	}
	if got, want := kinds(st.Chapters[1]), []string{"birth", "rename"}; !reflect.DeepEqual(got, want) {
		t.Errorf("chapter 2 moments %v, want %v", got, want)
	}
	if got, want := kinds(st.Chapters[2]), []string{"birth", "move", "quiet", "signature", "death"}; !reflect.DeepEqual(got, want) {
		t.Errorf("chapter 3 moments %v, want %v", got, want)
	}
	texts := map[string]string{}
	for _, m := range st.Moments {
		texts[m.Kind.String()] = m.Text
	}
	for kind, want := range map[string]string{
		"rename":    "3 symbols renamed, including Del → Remove",
		"move":      "5 symbols moved from core to store",
		"signature": "2 exported signatures changed, including Fetch",
		"quiet":     "Work resumes after 30 quiet days",
		"death":     "core is gone; it once held 6 symbols",
	} {
		if texts[kind] != want {
			t.Errorf("%s moment %q, want %q", kind, texts[kind], want)
		}
	}
	// The busiest week is in chapter 1 or 2: seven commits a week, but the
	// first week starts on the first commit.
	busy := 0
	for _, m := range st.Moments {
		if m.Kind == Busiest {
			busy++
			if m.Count != 7 {
				t.Errorf("busiest week has %d commits, want 7", m.Count)
			}
		}
	}
	if busy != 1 {
		t.Errorf("%d busiest weeks, want 1", busy)
	}
}

func TestNarrativeStatesOnlyFacts(t *testing.T) {
	st := threeActs().story(t)
	for _, c := range st.Chapters {
		// Every number in the text is one of the chapter's facts, or a
		// percentage of edits, or part of a date or a moment.
		allowed := map[int]bool{}
		for _, n := range []int{c.Commits, c.Added, c.Removed, c.Live, c.LivePackages, c.Edits,
			percent(c.HottestEdits, c.Edits), percent(c.RunnerEdits, c.Edits)} {
			allowed[n] = true
		}
		for _, tm := range []time.Time{c.From, c.To} {
			allowed[tm.Day()], allowed[tm.Year()] = true, true
		}
		for _, m := range c.Moments {
			for _, d := range regexp.MustCompile(`\d+`).FindAllString(m.Text, -1) {
				n, _ := strconv.Atoi(d)
				allowed[n] = true
			}
		}
		for _, d := range regexp.MustCompile(`\d+`).FindAllString(c.Text, -1) {
			if n, _ := strconv.Atoi(d); !allowed[n] {
				t.Errorf("chapter %d states %d, which is not a fact: %s", c.Number, n, c.Text)
			}
		}
		for _, p := range []string{c.Hottest, c.Runner} {
			if p != "" && !strings.Contains(c.Text, p) {
				t.Errorf("chapter %d does not name %s: %s", c.Number, p, c.Text)
			}
		}
		// Only packages of the history are named.
		for _, w := range regexp.MustCompile(`\b(core|api|store|\w+/\w+)\b`).FindAllString(c.Text, -1) {
			if !slices.Contains([]string{"core", "api", "store"}, w) {
				t.Errorf("chapter %d names %q: %s", c.Number, w, c.Text)
			}
		}
	}
	got := st.Chapters[1].Text
	want := "Between Jan 19, 2025 and Jan 30, 2025, 12 commits landed. Most of the 17 semantic edits (100%) were in api. " +
		"4 symbols were added and 0 removed, leaving 10 live symbols in 2 packages. New package: api. 3 symbols renamed, including Del → Remove."
	if got != want {
		t.Errorf("chapter 2:\n got %s\nwant %s", got, want)
	}
}

// long is a deterministic pseudo-random history of two years, with work
// wandering between packages and an occasional new one.
func long() *synth {
	s := newSynth()
	seed := uint32(1)
	rnd := func(n int) int {
		seed = seed*1664525 + 1013904223
		return int(seed>>16) % n
	}
	pkgs := []string{"p0"}
	s.commit(time.Hour, s.add("p0", "F0"))
	focus := 0
	for i := 1; i < 400; i++ {
		if i%60 == 0 {
			p := fmt.Sprintf("p%d", len(pkgs))
			pkgs = append(pkgs, p)
			focus = len(pkgs) - 1
			s.commit(time.Duration(rnd(48)+1)*time.Hour, s.add(p, "F0"))
			continue
		}
		p := pkgs[focus]
		if rnd(4) == 0 {
			p = pkgs[rnd(len(pkgs))]
		}
		gap := time.Duration(rnd(40)+1) * time.Hour
		if rnd(50) == 0 {
			gap = time.Duration(rnd(20)+10) * 24 * time.Hour
		}
		name := fmt.Sprintf("F%d", rnd(5))
		if s.live[p+"/"+name] == nil {
			s.commit(gap, s.add(p, name))
		} else {
			s.commit(gap, s.edit(p, name))
		}
	}
	return s
}

func TestLongHistory(t *testing.T) {
	st := long().story(t)
	if n := len(st.Chapters); n < minChapters || n > maxChapters {
		t.Fatalf("%d chapters", n)
	}
	next := 0
	for _, c := range st.Chapters {
		if c.FirstRev != next || c.LastRev < c.FirstRev {
			t.Errorf("chapter %d covers %d-%d, want to start at %d", c.Number, c.FirstRev, c.LastRev, next)
		}
		next = c.LastRev + 1
		if c.Title == "" || c.Text == "" {
			t.Errorf("chapter %d is not narrated", c.Number)
		}
		for _, m := range c.Moments {
			if m.Rev < c.FirstRev || m.Rev > c.LastRev {
				t.Errorf("chapter %d holds a moment of revision %d", c.Number, m.Rev)
			}
		}
	}
	if next != 400 {
		t.Errorf("chapters end at %d, want 400", next)
	}
	if again := long().story(t); !reflect.DeepEqual(again, st) {
		t.Error("the same history told a different story")
	}
}

func TestShortHistories(t *testing.T) {
	for n := 0; n <= 4; n++ {
		s := newSynth()
		for i := range n {
			s.commit(time.Hour, s.add("p", fmt.Sprintf("F%d", i)))
		}
		st := s.story(t)
		want := min(n, minChapters)
		if n == 4 {
			want = minChapters
		}
		if len(st.Chapters) != want {
			t.Errorf("%d commits: %d chapters, want %d", n, len(st.Chapters), want)
		}
	}
}

// echo is a narrator that only wraps what it is given, as a model-backed one
// would.
type echo struct{ inner Narrator }

func (e echo) Narrate(ctx context.Context, f Facts) (Narration, error) {
	n, err := e.inner.Narrate(ctx, f)
	n.Title = strings.ToUpper(n.Title)
	return n, err
}

func TestNarratorIsPluggable(t *testing.T) {
	s := threeActs()
	j := journey.Build(s.h, codemap.Build(s.h), journey.Commit, journey.Options{})
	st, err := Build(context.Background(), s.h, j, echo{Templates{}})
	if err != nil {
		t.Fatal(err)
	}
	if st.Chapters[1].Title != "ENTER API" {
		t.Errorf("title %q", st.Chapters[1].Title)
	}
}
