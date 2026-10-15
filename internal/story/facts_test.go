package story

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"huginn/internal/ast"
)

// imports makes dir/name refer to the packages of module example.com/m in
// dirs, and nothing else.
func (s *synth) imports(dir, name string, dirs ...string) ast.Change {
	o := s.live[dir+"/"+name]
	n := *o
	n.Refs = nil
	for _, d := range dirs {
		n.Refs = append(n.Refs, ast.Ref{Kind: ast.RefImport, Pkg: "example.com/m/" + d, Name: "X", Count: 1})
	}
	n.Stmts++
	s.live[dir+"/"+name] = &n
	return ast.Change{Flags: ast.BodyChanged, Old: o, New: &n}
}

// tag releases the last commit as name.
func (s *synth) tag(name string) {
	s.releases = append(s.releases, Release{Name: name, SHA: fmt.Sprintf("c%03d", s.n)})
}

// release is a history of three releases, a day between commits:
//
//	revs 0-9:   ann writes core with feat commits; v0.1.0
//	revs 10-19: api is born; bob and cy join with fixes, some scoped to
//	            api, and merged pull requests about tokens; api starts depending
//	            on core (rev 14); two exported signatures change (rev 17);
//	            v0.2.0
//	revs 20-29: store is born; core starts depending on api (rev 22),
//	            which makes a cycle until it stops again (rev 26); v1.0.0,
//	            and a nested module's tool/v0.1.0 on the same commit
func release() *synth {
	s := newSynth()
	const day = 24 * time.Hour
	s.subject = "feat(core): parse the config"
	s.commit(day, s.add("core", "Parse"), s.add("core", "Load"), s.add("core", "Save"))
	for i := 1; i < 10; i++ {
		s.subject = "feat(core): load configuration " + fmt.Sprint(i)
		s.commit(day, s.edit("core", "Parse"))
	}
	s.tag("v0.1.0")

	s.author, s.subject = "bob", "feat(api): token endpoint"
	s.commit(day, s.add("api", "Token"), s.add("api", "Refresh"))
	for i := 11; i < 20; i++ {
		switch {
		case i == 12:
			s.author, s.subject = "cy", "fix(api): refresh expired token"
			s.commit(day, s.edit("api", "Refresh"))
		case i == 14:
			s.author, s.subject = "bob", "fix(api): token parser uses core"
			s.commit(day, s.imports("api", "Token", "core"))
		case i == 17:
			s.author, s.subject, s.detail = "bob", "Merge pull request #7 from bob/sig", "Token takes a context"
			s.commit(day, s.resign("api", "Token"), s.resign("api", "Refresh"))
		case i%2 == 0:
			s.author, s.subject = "cy", fmt.Sprintf("fix(api): token expiry (#%d)", i)
			s.commit(day, s.edit("api", "Refresh"))
		default:
			s.author, s.subject = "bob", "fix: refresh tokens"
			s.commit(day, s.edit("api", "Token"))
		}
	}
	s.tag("v0.2.0")

	s.author, s.subject = "dee", "store: open the database"
	s.commit(day, s.add("store", "Open"), s.add("store", "Close"))
	for i := 21; i < 30; i++ {
		switch i {
		case 22:
			s.subject = "core: call the api"
			s.commit(day, s.imports("core", "Load", "api"))
		case 26:
			s.subject = "core: stop calling the api"
			s.commit(day, s.imports("core", "Load"))
		default:
			s.author, s.subject = "dee", "store: database migrations"
			s.commit(day, s.edit("store", "Open"))
		}
	}
	s.tag("tool/v0.1.0")
	s.tag("v1.0.0")
	return s
}

func TestReleaseFacts(t *testing.T) {
	s := release()
	st := s.story(t)
	var spans [][2]int
	for _, c := range st.Chapters {
		spans = append(spans, [2]int{c.FirstRev, c.LastRev})
	}
	if want := [][2]int{{0, 9}, {10, 19}, {20, 29}}; !reflect.DeepEqual(spans, want) {
		t.Fatalf("chapters %v, want %v", spans, want)
	}

	var rel []string
	for _, m := range st.Releases {
		rel = append(rel, fmt.Sprintf("%d %s: %s", m.Rev, m.Tag, m.Text))
	}
	want := []string{
		"9 v0.1.0: v0.1.0 released: 3 symbols added since the first commit",
		"19 v0.2.0: v0.2.0 released: 2 symbols added since v0.1.0, 2 exported signatures changed",
		"29 v1.0.0: v1.0.0 released: 2 symbols added since v0.2.0",
	}
	if !reflect.DeepEqual(rel, want) {
		t.Errorf("releases:\n got %q\nwant %q", rel, want)
	}
	for i, c := range st.Chapters {
		if !reflect.DeepEqual(c.Releases, []string{[]string{"v0.1.0", "v0.2.0", "v1.0.0"}[i]}) {
			t.Errorf("chapter %d releases %v", c.Number, c.Releases)
		}
	}
	if got := st.Chapters[2].Title; got != "Enter store" {
		t.Errorf("chapter 3 title %q", got)
	}
}

func TestThemeFacts(t *testing.T) {
	st := release().story(t)
	got := []Themes{st.Chapters[0].Themes, st.Chapters[1].Themes, st.Chapters[2].Themes}
	want := []Themes{
		{
			Types: []Theme{{"feat", 10}}, Typed: 10, LeadScope: "core", LeadScopeCount: 10,
			Scopes: []Theme{{"core", 10}},
		},
		{
			Types: []Theme{{"fix", 8}, {"feat", 1}}, Typed: 9, LeadScope: "api", LeadScopeCount: 4,
			Scopes:       []Theme{{"api", 5}},
			Terms:        []Theme{{"token", 10}, {"refresh", 5}, {"expiry", 2}},
			PullRequests: 3,
		},
		{
			Scopes: []Theme{{"store", 8}, {"core", 2}},
			Terms:  []Theme{{"database", 8}, {"migrations", 7}, {"api", 2}},
		},
	}
	// Chapter 1's subjects all say "load configuration" but one; "load"
	// is as common, and a tie goes by name.
	want[0].Terms = []Theme{{"configuration", 9}, {"load", 9}}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("chapter %d themes:\n got %+v\nwant %+v", i+1, got[i], want[i])
		}
	}
	text := st.Chapters[1].Text
	for _, s := range []string{
		"Most commits were fixes (8 of 10), 4 of them scoped to api.",
		`Recurring words in commit messages: "token" (10), "refresh" (5) and "expiry" (2).`,
		"3 pull requests were merged.",
	} {
		if !strings.Contains(text, s) {
			t.Errorf("chapter 2 does not say %q: %s", s, text)
		}
	}
	if s := "Commit messages most often named store (8) and core (2)."; !strings.Contains(st.Chapters[2].Text, s) {
		t.Errorf("chapter 3 does not say %q: %s", s, st.Chapters[2].Text)
	}
}

func TestPeopleFacts(t *testing.T) {
	st := release().story(t)
	type people struct {
		authors      int
		contributors []Contributor
		newcomers    []string
	}
	var got []people
	for _, c := range st.Chapters {
		got = append(got, people{c.Authors, c.Contributors, c.Newcomers})
	}
	want := []people{
		{1, []Contributor{{"ann", 10, 12}}, []string{"ann"}},
		{2, []Contributor{{"bob", 7, 9}, {"cy", 3, 3}}, []string{"bob", "cy"}},
		{2, []Contributor{{"dee", 8, 9}, {"ann", 2, 2}}, []string{"dee"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("people:\n got %+v\nwant %+v", got, want)
	}
	var newcomers []string
	for _, m := range st.Moments {
		if m.Kind == Newcomer {
			newcomers = append(newcomers, fmt.Sprintf("%d %s", m.Rev, m.Text))
		}
	}
	if want := []string{"10 First commit by bob, with 2 semantic edits", "12 First commit by cy, with 1 semantic edit", "20 First commit by dee, with 2 semantic edits"}; !reflect.DeepEqual(newcomers, want) {
		t.Errorf("newcomer moments:\n got %q\nwant %q", newcomers, want)
	}
	for _, s := range []string{"2 people committed.", "Of the 12 semantic edits, bob made 9 and cy 3.", "First commits by bob and cy."} {
		if !strings.Contains(st.Chapters[1].Text, s) {
			t.Errorf("chapter 2 does not say %q: %s", s, st.Chapters[1].Text)
		}
	}
	if strings.Contains(st.Chapters[0].Text, "committed") {
		t.Errorf("chapter 1 talks about its one author: %s", st.Chapters[0].Text)
	}
}

func TestDependencyMoments(t *testing.T) {
	st := release().story(t)
	var got []string
	for _, m := range st.Moments {
		switch m.Kind {
		case DepAdded, DepRemoved, CycleIn, CycleOut:
			got = append(got, fmt.Sprintf("%d %s: %s", m.Rev, m.Kind, m.Text))
		}
	}
	want := []string{
		"14 dep-added: api starts depending on core",
		"22 dep-added: core starts depending on api",
		"22 cycle: A dependency cycle appears between api and core",
		"26 dep-removed: core stops depending on api",
		"26 cycle-gone: No dependency cycle remains",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dependency moments:\n got %q\nwant %q", got, want)
	}
}

// TestReleaseEndsChapter checks that a release moves a chapter boundary to
// just after it, in a history that otherwise has no shift to cut at.
func TestReleaseEndsChapter(t *testing.T) {
	steady := func(tag bool) []int {
		s := newSynth()
		s.commit(time.Hour, s.add("p", "F"))
		for i := 1; i < 40; i++ {
			s.commit(24*time.Hour, s.edit("p", "F"))
			if tag && i == 24 {
				s.tag("v1.0.0")
			}
		}
		var starts []int
		for _, c := range s.story(t).Chapters {
			starts = append(starts, c.FirstRev)
		}
		return starts
	}
	if slices.Contains(steady(false), 25) {
		t.Fatalf("without a release a chapter already starts at 25: %v", steady(false))
	}
	if got := steady(true); !slices.Contains(got, 25) {
		t.Errorf("chapters start at %v, want one at 25, after v1.0.0", got)
	}
}

func TestParseTag(t *testing.T) {
	for _, c := range []struct {
		name string
		kind releaseKind
	}{
		{"v2.0.0", majorRelease}, {"v1.3.0", minorRelease}, {"v0.3.0", minorRelease}, {"1.2", minorRelease},
		{"v1.3.2", patchRelease}, {"go1.21.0", minorRelease}, {"v1.0.0-rc.1", preRelease},
		{"latest", otherTag}, {"tools/v0.1.0", minorRelease},
	} {
		if got := parseTag(c.name); got.kind != c.kind {
			t.Errorf("%s: kind %d, want %d", c.name, got.kind, c.kind)
		}
	}
	if !parseTag("tools/v0.1.0").nested || parseTag("v0.1.0").nested {
		t.Error("nested modules")
	}
}

func TestReadMessage(t *testing.T) {
	for _, c := range []struct {
		subject, detail string
		want            message
	}{
		{"feat(parser)!: accept generics", "", message{kind: "feat", scopes: []string{"parser"}, terms: []string{"accept", "generics"}}},
		{"cmd/go, runtime: speed up maps", "", message{scopes: []string{"cmd/go", "runtime"}, terms: []string{"speed", "maps"}}},
		{"Merge pull request #12 from a/b", "Add the journey view", message{pr: true, terms: []string{"journey", "view"}}},
		{"Bump github.com/x/y from 1.2.0 to 1.3.0 (#99)", "", message{pr: true}},
		{"Merge branch 'master' into dev", "", message{}},
		{"Revert \"fix: x\"", "", message{kind: "revert"}},
		{"docs: fix typo in README", "", message{kind: "docs"}},
	} {
		if got := readMessage(c.subject, c.detail); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got %+v, want %+v", c.subject, got, c.want)
		}
	}
}

// TestBackdatedCommitEndsNoPause checks that a merged commit dated long
// before its predecessor does not make the next commit end a pause.
func TestBackdatedCommitEndsNoPause(t *testing.T) {
	s := newSynth()
	s.commit(time.Hour, s.add("p", "F"))
	for i := 1; i < 20; i++ {
		gap := 24 * time.Hour
		switch i {
		case 10:
			gap = -200 * 24 * time.Hour // authored long ago, merged now
		case 11:
			gap = 201 * 24 * time.Hour
		}
		s.commit(gap, s.edit("p", "F"))
	}
	for _, m := range s.story(t).Moments {
		if m.Kind == Quiet {
			t.Errorf("revision %d: %s", m.Rev, m.Text)
		}
	}
}
