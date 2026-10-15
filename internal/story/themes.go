package story

import (
	"cmp"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"huginn/internal/lineage"
)

// Theme is something commit messages say, and in how many commits.
type Theme struct {
	Name  string
	Count int
}

// Themes are what a chapter's commit messages are about, read
// deterministically from their subjects.
type Themes struct {
	// Types are conventional-commit types (feat, fix, ...), most common
	// first, and Typed is how many commits had one.
	Types []Theme
	Typed int

	// LeadScope is the most common scope among the commits of Types[0], and
	// LeadScopeCount how many of them had it.
	LeadScope      string
	LeadScopeCount int

	// Scopes are the areas subjects name, most common first: conventional
	// scopes, "feat(parser): ...", and Go-style prefixes, "parser: ...".
	Scopes []Theme

	// Terms are the words that recur in the subjects, most distinctive of
	// the chapter first.
	Terms []Theme

	// PullRequests counts merged pull requests: merge commits named
	// "Merge pull request #N" and squashed ones ending in "(#N)".
	PullRequests int
}

// commitTypes are the conventional-commit types recognized. A prefix that
// is not one of them names a scope, Go style.
var commitTypes = map[string]bool{
	"feat": true, "fix": true, "docs": true, "doc": true, "style": true, "refactor": true, "perf": true,
	"test": true, "tests": true, "build": true, "ci": true, "chore": true, "revert": true, "deps": true,
}

var (
	conventional = regexp.MustCompile(`^([A-Za-z]+)(?:\(([^)]*)\))?!?:\s*(.*)$`)
	goPrefix     = regexp.MustCompile(`^([\w./-]+(?:\s*,\s*[\w./-]+)*):\s+(.*)$`)
	mergedPR     = regexp.MustCompile(`^Merge pull request #\d+`)
	squashedPR   = regexp.MustCompile(`\s*\(#\d+\)$`)
)

// message is one commit message, read.
type message struct {
	kind   string   // conventional type, if any
	scopes []string // conventional scopes or Go-style prefixes
	terms  []string // distinct meaningful words
	pr     bool     // a merged pull request
}

// readMessage reads a commit's subject. A merged pull request is read by its
// title, the line after the merge subject.
func readMessage(subject, detail string) message {
	var m message
	s := subject
	if mergedPR.MatchString(s) {
		m.pr = true
		s = detail
	}
	if loc := squashedPR.FindStringIndex(s); loc != nil {
		m.pr = true
		s = s[:loc[0]]
	}
	if strings.HasPrefix(s, "Merge ") || strings.HasPrefix(s, "Revert \"") {
		// Merges of branches say nothing about the work; reverts say it
		// again.
		if strings.HasPrefix(s, "Revert") {
			m.kind = "revert"
		}
		return m
	}
	if c := conventional.FindStringSubmatch(s); c != nil && commitTypes[strings.ToLower(c[1])] {
		m.kind = strings.ToLower(c[1])
		switch m.kind {
		case "doc":
			m.kind = "docs"
		case "tests":
			m.kind = "test"
		}
		m.scopes = splitScopes(c[2])
		s = c[3]
	} else if g := goPrefix.FindStringSubmatch(s); g != nil {
		m.scopes = splitScopes(g[1])
		s = g[2]
	}
	m.terms = terms(s)
	return m
}

func splitScopes(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		p = strings.ToLower(strings.Trim(p, "/"))
		if p != "" && p != "*" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// terms returns the distinct meaningful words of s, lowercased. Paths,
// URLs, versions and other dotted names are skipped whole: their parts
// (github, com, v1) say little on their own.
func terms(s string) []string {
	var out []string
	var words []string
	for _, f := range strings.Fields(s) {
		f = strings.TrimRight(f, ".,;:!?)\"'`")
		if strings.ContainsAny(f, "/.@") {
			continue
		}
		words = append(words, strings.FieldsFunc(f, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })...)
	}
	for _, w := range words {
		w = strings.ToLower(w)
		if len(w) < 3 || stopWords[w] || isNumberOrHash(w) || slices.Contains(out, w) {
			continue
		}
		out = append(out, w)
	}
	return out
}

func isNumberOrHash(w string) bool {
	digits, hex := 0, true
	for _, r := range w {
		switch {
		case '0' <= r && r <= '9':
			digits++
		case 'a' <= r && r <= 'f':
		default:
			hex = false
		}
	}
	return digits == len(w) || hex && digits > 0 && len(w) >= 7
}

// stopWords are English function words and the verbs and nouns every
// commit message uses, which say nothing about what a chapter was about.
var stopWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
		the and for with from into onto that this these those then than when where which while
		are was were been being has have had not but all any some can could should would will
		its it's our out off via per use used uses using also only just more most less now new
		add adds added adding update updates updated updating remove removes removed removing
		fix fixes fixed fixing make makes made making change changes changed changing
		move moves moved moving rename renames renamed renaming improve improves improved
		support supports supported allow allows allowed handle handles handled ensure ensures
		bump bumps bumped set sets get gets let lets don't doesn't instead after before
		initial first second minor small more better some other various misc etc wip
		merge merged branch master main pull request issue issues commit commits
		code file files test tests version release refactor refactors refactored cleanup clean
		docs doc readme typo typos comment comments
	`) {
		m[w] = true
	}
	return m
}()

// maxThemes is how many types, scopes and terms a chapter keeps of each.
const maxThemes = 3

// themes reads the messages of the revisions [from, to). df counts, per
// term, the chapters it appears in, and chapters is how many there are;
// they make the terms distinctive of the chapter.
func themes(msgs []message, from, to int, df map[string]int, chapters int) Themes {
	var t Themes
	types, scopes, words := map[string]int{}, map[string]int{}, map[string]int{}
	typeScopes := map[[2]string]int{}
	for _, m := range msgs[from:to] {
		if m.pr {
			t.PullRequests++
		}
		if m.kind != "" {
			t.Typed++
			types[m.kind]++
		}
		for _, s := range m.scopes {
			scopes[s]++
			if m.kind != "" {
				typeScopes[[2]string{m.kind, s}]++
			}
		}
		for _, w := range m.terms {
			words[w]++
		}
	}
	foldPlurals(words)
	t.Types = top(types, 1, nil)
	t.Scopes = top(scopes, 2, nil)
	if len(t.Types) > 0 {
		lead := t.Types[0].Name
		for k, n := range typeScopes {
			if k[0] == lead && (n > t.LeadScopeCount || n == t.LeadScopeCount && k[1] < t.LeadScope) {
				t.LeadScope, t.LeadScopeCount = k[1], n
			}
		}
	}
	// A term must recur: in two commits at least, and in one of every 50.
	least := max(2, (to-from+49)/50)
	t.Terms = top(words, least, func(w string, n int) float64 {
		return float64(n) * math.Log(1+float64(chapters)/float64(max(1, df[strings.TrimSuffix(w, "s")])))
	})
	// Chosen for being distinctive, told most common first.
	slices.SortStableFunc(t.Terms, func(a, b Theme) int { return cmp.Compare(b.Count, a.Count) })
	return t
}

// foldPlurals counts "tests" as "test" when both occur.
func foldPlurals(words map[string]int) {
	for _, w := range slices.Sorted(func(yield func(string) bool) {
		for w := range words {
			if !yield(w) {
				return
			}
		}
	}) {
		if one, ok := strings.CutSuffix(w, "s"); ok && words[one] > 0 {
			words[one] += words[w]
			delete(words, w)
		}
	}
}

// top returns up to maxThemes of counts that reach least, by score (the
// count by default), then count, then name.
func top(counts map[string]int, least int, score func(string, int) float64) []Theme {
	if score == nil {
		score = func(_ string, n int) float64 { return float64(n) }
	}
	var out []Theme
	for k, n := range counts {
		if n >= least {
			out = append(out, Theme{k, n})
		}
	}
	slices.SortFunc(out, func(a, b Theme) int {
		return cmp.Or(cmp.Compare(score(b.Name, b.Count), score(a.Name, a.Count)), cmp.Compare(b.Count, a.Count), cmp.Compare(a.Name, b.Name))
	})
	return out[:min(len(out), maxThemes)]
}

// messages reads every revision's message.
func messages(h *lineage.History) []message {
	out := make([]message, h.Revisions())
	for i := range out {
		out[i] = readMessage(h.MessageAt(i))
	}
	return out
}

// termChapters counts, per term, the chapters whose revisions use it.
func termChapters(msgs []message, spans [][2]int) map[string]int {
	df := map[string]int{}
	for _, s := range spans {
		seen := map[string]bool{}
		for _, m := range msgs[s[0]:s[1]] {
			for _, w := range m.terms {
				seen[strings.TrimSuffix(w, "s")] = true
			}
		}
		for w := range seen {
			df[w]++
		}
	}
	return df
}
