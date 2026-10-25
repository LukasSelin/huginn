package health

import (
	"regexp"
	"strings"
	"unicode"
)

// Intent is what a commit's message says the commit was for.
type Intent uint8

const (
	Other   Intent = iota // anything else: refactors, docs, chores, unclear
	Merge                 // a merge of a branch that says nothing about the work
	Feature               // new behavior
	Fix                   // a bug fix
	Revert                // undoing an earlier commit
)

var (
	conventional = regexp.MustCompile(`^([A-Za-z]+)(?:\([^)]*\))?!?:`)
	mergedPR     = regexp.MustCompile(`^Merge pull request #\d+`)
)

// fixWords are words that mark a subject as a fix wherever they occur;
// fixVerbs and featureVerbs only when they open it, as imperative subjects
// do ("Stop a cancelled walk from hanging").
var (
	fixWords     = words("bug bugs bugfix hotfix crash crashes regression broken")
	fixVerbs     = words("fix fixes fixed hotfix repair correct resolve prevent stop avoid")
	featureVerbs = words("add adds added introduce introduces implement implements support supports")
)

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// ReadIntent reads a commit's subject: by its conventional-commit type when
// it has one ("fix(parser): ..."), by its opening verb and telling words
// otherwise. A merged pull request is read by its title, the line after the
// merge subject.
func ReadIntent(subject, detail string) Intent {
	s := subject
	if mergedPR.MatchString(s) {
		s = detail
	}
	switch {
	case strings.HasPrefix(s, "Revert"):
		return Revert
	case strings.HasPrefix(s, "Merge "):
		return Merge
	}
	if c := conventional.FindStringSubmatch(s); c != nil {
		switch strings.ToLower(c[1]) {
		case "fix", "hotfix", "bugfix":
			return Fix
		case "feat", "feature":
			return Feature
		case "revert":
			return Revert
		case "docs", "doc", "style", "refactor", "perf", "test", "tests", "build", "ci", "chore", "deps":
			return Other
		default:
			s = s[len(c[0]):] // a Go-style scope, "parser: fix ..."
		}
	}
	ws := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) })
	if len(ws) == 0 {
		return Other
	}
	for _, w := range ws {
		if fixWords[w] {
			return Fix
		}
	}
	switch {
	case fixVerbs[ws[0]]:
		return Fix
	case featureVerbs[ws[0]]:
		return Feature
	}
	return Other
}

// fixes reports whether the commit repaired or undid earlier work.
func (i Intent) fixes() bool { return i == Fix || i == Revert }
