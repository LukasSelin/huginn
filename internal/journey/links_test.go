package journey

import (
	"fmt"
	"slices"
	"testing"
)

func TestLinksAt(t *testing.T) {
	h := fixture()
	str := func(ls []Link) []string {
		var out []string
		for _, l := range ls {
			out = append(out, fmt.Sprintf("%s.%s → %s %v ×%d", l.From, l.Name, l.To, l.Names, l.Refs))
		}
		return out
	}

	// After b: a.G refers to b; nothing refers to a yet.
	l := LinksAt(h, "b", 1, false)
	if got, want := str(l.In), []string{"a.G → b [X] ×1"}; !slices.Equal(got, want) || len(l.Out) != 0 {
		t.Errorf("b at 1: in %q, out %q", got, str(l.Out))
	}
	if len(l.Symbols) != 1 || l.Symbols[0].Name != "H" {
		t.Errorf("b at 1: symbols %+v", l.Symbols)
	}

	// After d: b.I refers back to a.
	l = LinksAt(h, "a", 3, false)
	if got, want := str(l.Out), []string{"a.G → b [X] ×1"}; !slices.Equal(got, want) {
		t.Errorf("a at 3: out %q", got)
	}
	if got, want := str(l.In), []string{"b.I → a [X] ×1"}; !slices.Equal(got, want) {
		t.Errorf("a at 3: in %q", got)
	}
	var names []string
	for _, s := range l.Symbols {
		names = append(names, s.Name)
	}
	if want := []string{"F", "G"}; !slices.Equal(names, want) {
		t.Errorf("a at 3: symbols %q, want %q (tests left out)", names, want)
	}

	// Before the first revision nothing is live.
	if l := LinksAt(h, "a", -1, false); len(l.Symbols)+len(l.In)+len(l.Out) != 0 {
		t.Errorf("a at -1 = %+v", l)
	}
}
