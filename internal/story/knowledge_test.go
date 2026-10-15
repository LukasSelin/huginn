package story

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"huginn/internal/journey"
)

// handover is a history where core changes hands and store is left
// behind, a day between commits unless said otherwise:
//
//	revs 0-1: ann writes core; cy writes store
//	rev 2:    bob rewrites core.A, which makes him core's top knower
//	rev 3:    a week on, bob edits core.B
//	rev 4:    seven months on, bob adds core.C; ann and cy have been away
//	          for six months, and store is cy's alone
func handover() *synth {
	s := newSynth()
	const day = 24 * time.Hour
	s.commit(day, s.add("core", "A"), s.add("core", "B"))
	s.author = "cy"
	s.commit(day, s.add("store", "Open"))
	s.author = "bob"
	s.commit(day, s.edit("core", "A"))
	s.author = "bob"
	s.commit(7*day, s.edit("core", "B"))
	s.author = "bob"
	s.commit(210*day, s.add("core", "C"))
	return s
}

func TestKnowledgeMoments(t *testing.T) {
	st := handover().story(t)
	var got []string
	for _, m := range st.Moments {
		if m.Kind == Handoff || m.Kind == Orphaned {
			got = append(got, fmt.Sprintf("%d %s %s", m.Rev, m.Kind, m.Text))
		}
	}
	want := []string{
		"2 handoff bob now knows core best, holding 67%; ann did before",
		"4 orphaned 100% of what is known of store is held by people who have not committed in six months",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("knowledge moments:\n got %q\nwant %q", got, want)
	}
}

func TestHandoffNeedsAShare(t *testing.T) {
	j := &journey.Journey{
		Packages: []journey.Package{{Path: "a"}},
		Authors:  []string{"ann", "bob", "cy"},
	}
	frame := func(orphaned float64, ks ...journey.Knower) journey.Frame {
		return journey.Frame{Packages: []journey.PackageState{{Package: 0, Live: 1, Knowers: ks, Orphaned: orphaned}}}
	}
	j.Frames = []journey.Frame{
		frame(0, journey.Knower{Author: 0, Share: 1}),
		// bob tops ann but holds too little to take over.
		frame(0, journey.Knower{Author: 1, Share: 0.35}, journey.Knower{Author: 0, Share: 0.34}),
		frame(0.6, journey.Knower{Author: 2, Share: 0.45}),
		// Orphaned again: only the first time counts.
		frame(0, journey.Knower{Author: 2, Share: 0.5}),
		frame(0.7, journey.Knower{Author: 2, Share: 0.5}),
	}
	var got []string
	for _, m := range knowledgeMoments(j, []string{"a"}) {
		got = append(got, fmt.Sprintf("%d %s %d", m.Rev, m.Kind, m.Count))
	}
	if want := []string{"2 handoff 45", "2 orphaned 60"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
