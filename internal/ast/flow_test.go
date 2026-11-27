package ast

import (
	"fmt"
	"strings"
	"testing"
)

// outline writes a flow one step a line, indented by depth: "kind text".
func outline(steps []Step) string {
	var b strings.Builder
	for _, s := range steps {
		fmt.Fprintf(&b, "%s%s %s\n", strings.Repeat("  ", s.Depth), s.Kind, s)
	}
	return b.String()
}

func TestFlows(t *testing.T) {
	src := `package rv

import (
	"errors"
	"os"
)

type Repo struct{ pending *string }

func (r *Repo) Target(base string, getenv func(string) string) (string, error) {
	if base != "" {
		return base, nil
	}
	if getenv != nil {
		for _, v := range []string{"A", "B"} {
			if b := getenv(v); b != "" {
				return "origin/" + b, nil
			}
		}
	}
	b, err := r.DefaultBranch()
	if err != nil {
		return "", errors.Join(err, errors.New("name the branch"))
	}
	r.pending = &b
	return b, nil
}

func (r *Repo) DefaultBranch() (string, error) { return "", nil }

func clean(dir string, done chan<- bool) {
	defer func() {
		if err := os.Remove(dir); err != nil {
			panic(err)
		}
	}()
	go notify(done)
	_ = os.Chmod(dir, 0o750)
	done <- true
	for i := 0; i < 3; i++ {
		continue
	}
}

func notify(chan<- bool) {}

func init() {}
func init() {}
`
	flows := Flows("rv.go", []byte(src))

	want := `decision if base != "" → return base, nil
decision if getenv != nil
  loop for _, v := range []string{…}
    decision if b := getenv(v); b != "" → return "origin/" + b, nil
effect err ← r.DefaultBranch(…)
decision if err != nil → return "", errors.Join(err, errors.New("name the branch"))
effect writes r.pending
exit return b, nil
`
	if got := outline(flows["Repo.Target"]); got != want {
		t.Errorf("Repo.Target:\n got:\n%s\nwant:\n%s", got, want)
	}
	if s := flows["Repo.Target"][3]; s.Cond != `b := getenv(v); b != ""` || s.Line != 16 {
		t.Errorf("step 3: Cond %q, line %d", s.Cond, s.Line)
	}
	if s := flows["Repo.Target"][6]; s.Key != "writes ʳ.pending" {
		t.Errorf("the receiver is written ʳ in keys: %q", s.Key)
	}

	want = `closure defer func literal
  decision if err := os.Remove(dir); err != nil → panic(err)
effect go notify(…)
effect drops a result of os.Chmod(…)
effect I/O os.Chmod(…)
effect send on done
loop for i := 0; i < 3; i++
  jump continue
`
	if got := outline(flows["clean"]); got != want {
		t.Errorf("clean:\n got:\n%s\nwant:\n%s", got, want)
	}

	for _, k := range []string{"init", "init#2", "Repo.DefaultBranch"} {
		if _, ok := flows[k]; !ok {
			t.Errorf("no flow keyed %q; keys %v", k, keys(flows))
		}
	}
}

func keys(m map[string][]Step) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestFlowsKeys(t *testing.T) {
	// A switch on a tag and the same switch without one, with the receiver
	// named differently: every case has the same key on both sides.
	before := Flows("a.go", []byte(`package p
type T struct{ kind int }
func (t T) rank() int {
	switch {
	case t.kind == 1:
		return 1
	case t.kind == 2, t.kind == 3:
		return 2
	}
	return 0
}`))["T.rank"]
	after := Flows("a.go", []byte(`package p
type T struct{ kind int }
func (x T) rank() int {
	switch x.kind {
	case 1:
		return 1
	case 2, 3:
		return 2
	}
	return 0
}`))["T.rank"]
	if len(before) != len(after) {
		t.Fatalf("before:\n%s\nafter:\n%s", outline(before), outline(after))
	}
	for i := range before {
		if before[i].Key != after[i].Key {
			t.Errorf("step %d: key %q before, %q after", i, before[i].Key, after[i].Key)
		}
	}
	if got := after[2].Text + " | " + after[2].Cond; got != "case 2, 3 | x.kind == 2 || x.kind == 3" {
		t.Errorf("case shows %q", got)
	}
	if after[0].Text != "switch x.kind" {
		t.Errorf("switch shows %q", after[0].Text)
	}
}

func TestFlowsClosures(t *testing.T) {
	flows := Flows("c.go", []byte(`package p
import (
	"net/http"
	"slices"
)
func handler(xs []int) http.HandlerFunc {
	less := func(a, b int) int {
		if a < b {
			return -1
		}
		return 1
	}
	slices.SortFunc(xs, func(a, b int) int { return less(b, a) })
	return func(w http.ResponseWriter, r *http.Request) {
		switch v := any(r.Body).(type) {
		case nil:
			http.Error(w, "no body", http.StatusBadRequest)
		case interface{ Close() error }, error:
			return
		}
	}
}`))
	want := `closure func literal → less
  decision if a < b → return -1
  exit return 1
closure func literal in slices.SortFunc
  exit return less(b, a)
exit return func literal
  closure func literal
    decision switch v := any(r.Body).(type)
      decision case nil
        effect I/O http.Error(…)
      decision case interface{Close() error}, error → return
`
	if got := outline(flows["handler"]); got != want {
		t.Errorf("handler:\n got:\n%s\nwant:\n%s", got, want)
	}
	if s := flows["handler"][10]; s.Cond != "any(r.Body) is interface{Close() error} or error" {
		t.Errorf("type switch case Cond %q", s.Cond)
	}
}
