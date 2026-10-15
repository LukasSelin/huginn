package story

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Limits on a model's narration.
const (
	maxTitleWords = 6
	maxTitleRunes = 60
	minSentences  = 2
	maxSentences  = 4
	maxTextRunes  = 800
)

// vocabulary is what a narration may draw on: the chapter's facts and the
// templated narration of them, gathered generically from their JSON so that
// new fields count without code changes.
type vocabulary struct {
	text    string          // every string, joined
	numbers map[string]bool // every number, as digits
	words   map[string]bool // every word, as written
}

var (
	numberRE = regexp.MustCompile(`\b\d[\d,]*(?:\.\d+)?\b`)
	wordRE   = regexp.MustCompile(`[\pL][\pL\pN'’-]*`)
	// A sentence ends at ., ! or ?, maybe closed by a quote or parenthesis,
	// before a space or the end.
	sentenceEndRE = regexp.MustCompile(`[.!?]["'”’)]*(?:\s|$)`)
	// Code-like tokens: paths, qualified or receiver names, snake_case and
	// camelCase identifiers.
	codeRE = regexp.MustCompile(`[/_*()]|\pL\.\pL|\p{Ll}\p{Lu}`)
)

func newVocabulary(f Facts, base Narration) (*vocabulary, error) {
	raw, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, err
	}
	v := &vocabulary{numbers: map[string]bool{}, words: map[string]bool{}}
	var b strings.Builder
	var walk func(any)
	walk = func(x any) {
		switch x := x.(type) {
		case map[string]any:
			for _, y := range x {
				walk(y)
			}
		case []any:
			for _, y := range x {
				walk(y)
			}
		case float64:
			v.numbers[strconv.FormatFloat(x, 'f', -1, 64)] = true
		case string:
			v.addString(&b, x)
		}
	}
	walk(tree)
	v.addString(&b, base.Title)
	v.addString(&b, base.Text)
	v.text = b.String()
	return v, nil
}

func (v *vocabulary) addString(b *strings.Builder, s string) {
	b.WriteString(s)
	b.WriteByte('\n')
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		// A timestamp may be told as a date: its year, month and day.
		for _, n := range []int{t.Year(), int(t.Month()), t.Day()} {
			v.numbers[strconv.Itoa(n)] = true
		}
		for _, w := range []string{t.Month().String(), t.Month().String()[:3], t.Weekday().String()} {
			v.words[w] = true
		}
		return
	}
	for _, n := range numberRE.FindAllString(s, -1) {
		v.numbers[normalNumber(n)] = true
	}
	for _, w := range wordRE.FindAllString(s, -1) {
		v.words[w] = true
	}
}

func normalNumber(n string) string {
	n = strings.ReplaceAll(n, ",", "")
	if f, err := strconv.ParseFloat(n, 64); err == nil {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return n
}

// numberWords are the spelled-out numbers a narration may use for a number
// in the facts. "one" is left out: prose uses it for other things.
var numberWords = map[string]string{
	"two": "2", "three": "3", "four": "4", "five": "5", "six": "6", "seven": "7", "eight": "8",
	"nine": "9", "ten": "10", "eleven": "11", "twelve": "12", "twenty": "20", "hundred": "100",
}

// properOK are capitalized words a narration may use mid-sentence without
// the facts naming them.
var properOK = map[string]bool{
	"Go": true, "Git": true, "API": true, "APIs": true, "JSON": true, "HTTP": true, "CLI": true, "AST": true, "I": true,
}

func init() {
	for m := time.January; m <= time.December; m++ {
		properOK[m.String()], properOK[m.String()[:3]] = true, true
	}
	for d := time.Sunday; d <= time.Saturday; d++ {
		properOK[d.String()] = true
	}
}

// validate checks a model's narration of f against the facts and the
// templated narration base, and returns what is wrong with it, if anything.
func validate(f Facts, base, n Narration) ([]string, error) {
	v, err := newVocabulary(f, base)
	if err != nil {
		return nil, err
	}
	var out []string
	bad := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	title, text := strings.TrimSpace(n.Title), strings.TrimSpace(n.Text)
	switch words := len(strings.Fields(title)); {
	case words == 0:
		bad("the title is empty")
	case words > maxTitleWords:
		bad("the title has %d words; at most %d are allowed", words, maxTitleWords)
	}
	if r := len([]rune(title)); r > maxTitleRunes {
		bad("the title has %d characters; at most %d are allowed", r, maxTitleRunes)
	}
	if strings.ContainsAny(title, "\n") || strings.HasSuffix(title, ".") {
		bad("the title must be one line without a final period")
	}
	switch s := len(sentenceEndRE.FindAllString(text+" ", -1)); {
	case s < minSentences || s > maxSentences:
		bad("the paragraph has %d sentences; it must have %d to %d", s, minSentences, maxSentences)
	}
	if r := len([]rune(text)); r > maxTextRunes {
		bad("the paragraph has %d characters; at most %d are allowed", r, maxTextRunes)
	}
	if strings.ContainsAny(title+text, "!") {
		bad("exclamation marks are not allowed")
	}

	seen := map[string]bool{}
	once := func(s string) bool {
		if seen[s] {
			return false
		}
		seen[s] = true
		return true
	}
	for _, part := range []string{title, text} {
		for _, n := range numberRE.FindAllString(part, -1) {
			if !v.numbers[normalNumber(n)] && once("n"+n) {
				bad("the number %s is not in the facts", n)
			}
		}
		for _, w := range wordRE.FindAllString(part, -1) {
			if d, ok := numberWords[strings.ToLower(w)]; ok && !v.numbers[d] && once("n"+w) {
				bad("the number %q (%s) is not in the facts", w, d)
			}
		}
		for _, tok := range strings.Fields(part) {
			tok = trimToken(tok)
			if codeRE.MatchString(tok) && !strings.Contains(v.text, tok) && once("c"+tok) {
				bad("%q is not a package or symbol named in the facts", tok)
			}
		}
		for _, w := range midSentenceCapitals(part) {
			if !v.words[w] && !properOK[w] && once("p"+w) {
				bad("%q looks like a name, but the facts do not contain it", w)
			}
		}
	}
	return out, nil
}

// trimToken strips the punctuation prose puts around a token, keeping the
// parentheses of a receiver like (*Repo).Walk.
func trimToken(t string) string {
	t = strings.Trim(t, "`\"'“”‘’,;:!?")
	t = strings.TrimRight(t, ".")
	if strings.HasPrefix(t, "(") && !strings.Contains(t, ")") {
		t = t[1:]
	}
	if strings.HasSuffix(t, ")") && !strings.Contains(t, "(") {
		t = t[:len(t)-1]
	}
	if strings.HasSuffix(t, ")") && strings.Count(t, ")") > strings.Count(t, "(") {
		t = t[:len(t)-1]
	}
	return strings.TrimRight(t, ".,;:")
}

// midSentenceCapitals returns the words that start with a capital letter
// but do not start a sentence, other than code-like ones. A name that starts
// a sentence goes unnoticed: telling it from an ordinary first word would
// take a dictionary. The prompt asks for no names, so this is a backstop.
func midSentenceCapitals(s string) []string {
	var out []string
	start := true
	for _, tok := range strings.Fields(s) {
		w := strings.TrimLeft(tok, "`\"'“‘(")
		if !start && w != "" && !codeRE.MatchString(trimToken(tok)) {
			r := []rune(w)
			if unicode.IsUpper(r[0]) {
				if m := wordRE.FindString(w); m != "" {
					out = append(out, strings.TrimRight(m, "'’-"))
				}
			}
		}
		start = sentenceEndRE.MatchString(tok + " ")
	}
	return out
}
