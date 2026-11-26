package journey

import (
	"reflect"
	"testing"
)

func TestLayers(t *testing.T) {
	tests := []struct {
		name   string
		n      int
		deps   []Dep
		layer  []int
		cyclic []bool
	}{
		{
			name:   "no deps",
			n:      3,
			layer:  []int{0, 0, 0},
			cyclic: []bool{},
		},
		{
			// 0 refers to 1 and 2, 1 to 2; 3 is on its own. 0 sits above
			// the longest chain below it, not the shortest.
			name:   "chain and shortcut",
			n:      4,
			deps:   []Dep{{From: 0, To: 1}, {From: 0, To: 2}, {From: 1, To: 2}},
			layer:  []int{2, 1, 0, 0},
			cyclic: []bool{false, false, false},
		},
		{
			// 0 and 1 refer to each other and share a layer above 2, which
			// 1 refers to; 3 refers to 0 and sits above the cycle.
			name:   "cycle",
			n:      4,
			deps:   []Dep{{From: 0, To: 1}, {From: 1, To: 0}, {From: 1, To: 2}, {From: 3, To: 0}},
			layer:  []int{1, 1, 0, 2},
			cyclic: []bool{true, true, false, false},
		},
		{
			// A cycle of three, entered from above and leaving below
			// through each of its members.
			name: "longer cycle",
			n:    5,
			deps: []Dep{
				{From: 0, To: 1}, {From: 1, To: 2}, {From: 2, To: 0},
				{From: 2, To: 3}, {From: 4, To: 1},
			},
			layer:  []int{1, 1, 1, 0, 2},
			cyclic: []bool{true, true, true, false, false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			layer, cyclic := Layers(tt.n, tt.deps)
			if !reflect.DeepEqual(layer, tt.layer) {
				t.Errorf("layers %v, want %v", layer, tt.layer)
			}
			if !reflect.DeepEqual(cyclic, tt.cyclic) {
				t.Errorf("cyclic %v, want %v", cyclic, tt.cyclic)
			}
			for i, d := range tt.deps {
				if !cyclic[i] && layer[d.From] <= layer[d.To] {
					t.Errorf("%d→%d points from layer %d to %d", d.From, d.To, layer[d.From], layer[d.To])
				}
			}
		})
	}
}

// The fixture's b comes to refer back to a, which Go allows only across
// tests or build variants; the two then share a layer.
func TestLayersOfFrames(t *testing.T) {
	j := build(Commit)
	want := []struct {
		layer  map[string]int
		cyclic []bool
	}{
		{map[string]int{"a": 0}, []bool{}},
		{map[string]int{"a": 1, "b": 0, "c": 0}, []bool{false}},
		{map[string]int{"a": 1, "b": 0}, []bool{false}},
		{map[string]int{"a": 0, "b": 0}, []bool{true, true}},
	}
	for i, f := range j.Frames {
		layer, cyclic := Layers(len(j.Packages), f.Deps)
		got := map[string]int{}
		for _, p := range f.Packages {
			if p.Live > 0 {
				got[j.Packages[p.Package].Path] = layer[p.Package]
			}
		}
		if !reflect.DeepEqual(got, want[i].layer) || !reflect.DeepEqual(cyclic, want[i].cyclic) {
			t.Errorf("frame %d: layers %v cyclic %v, want %v %v", i, got, cyclic, want[i].layer, want[i].cyclic)
		}
	}
}
