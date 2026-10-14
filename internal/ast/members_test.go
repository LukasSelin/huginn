package ast

import (
	"fmt"
	"testing"
)

func TestTypeMembers(t *testing.T) {
	f := mustParse(t, "a.go", `package a

import "io"

type Config struct {
	Name, Path string `+"`json:\"name\"`"+`
	*io.SectionReader
	sync.Mutex
	limit int
	_     [4]byte
}

type Store interface {
	io.Closer
	Get(ctx context.Context, key string) ([]byte, error)
	put(key string)
}

type Number interface {
	~int | ~float64
	comparable
}

type ID int
`)
	render := func(key string) []string {
		var out []string
		for _, m := range symbol(t, f, key).Members {
			out = append(out, fmt.Sprintf("%d %s %s %q exported=%v embedded=%v line=%d", m.Index, m.Kind, m.Name, m.String(), m.Exported, m.Embedded(), m.Line))
		}
		return out
	}
	for _, tc := range []struct {
		key  string
		want []string
	}{
		{"Config", []string{
			`0 field Name "Name string ` + "`json:\\\"name\\\"`" + `" exported=true embedded=false line=6`,
			`1 field Path "Path string ` + "`json:\\\"name\\\"`" + `" exported=true embedded=false line=6`,
			`2 embedded field SectionReader "*io.SectionReader" exported=true embedded=true line=7`,
			`3 embedded field Mutex "sync.Mutex" exported=true embedded=true line=8`,
			`4 field limit "limit int" exported=false embedded=false line=9`,
			`5 field _ "_ [4]byte" exported=false embedded=false line=10`,
		}},
		{"Store", []string{
			`0 embedded interface Closer "io.Closer" exported=true embedded=true line=14`,
			`1 method Get "Get(ctx context.Context, key string) ([]byte, error)" exported=true embedded=false line=15`,
			`2 method put "put(key string)" exported=false embedded=false line=16`,
		}},
		{"Number", []string{
			`0 type term  "~int | ~float64" exported=false embedded=false line=20`,
			`1 embedded interface comparable "comparable" exported=false embedded=true line=21`,
		}},
	} {
		if got := render(tc.key); fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s members:\n got %q\nwant %q", tc.key, got, tc.want)
		}
	}
	if ms := symbol(t, f, "ID").Members; ms != nil {
		t.Errorf("ID has members %+v", ms)
	}
}

// memberDiff diffs a one-type file and renders the member changes of the
// type's change.
func memberDiff(t *testing.T, oldSrc, newSrc string) []string {
	t.Helper()
	cs := diffOne(t, oldSrc, newSrc)
	if len(cs) != 1 {
		t.Fatalf("got %d changes, want 1: %+v", len(cs), cs)
	}
	if !cs[0].Flags.Has(BodyChanged) {
		t.Fatalf("flags = %v, want body", cs[0].Flags)
	}
	var out []string
	for _, mc := range cs[0].Members {
		s := mc.Flags.String() + " " + mc.Cur().Kind.String()
		if mc.Old != nil {
			s += " -" + mc.Old.String()
		}
		if mc.New != nil {
			s += " +" + mc.New.String()
		}
		out = append(out, s)
	}
	return out
}

func TestDiffMembersStruct(t *testing.T) {
	got := memberDiff(t, `package a

type User struct {
	ID    int
	Name  string `+"`json:\"name\"`"+`
	Email string
	Admin bool
}
`, `package a

type User struct {
	ID    int64
	Name  string `+"`json:\"full_name\"`"+`
	Email string
	Phone string
}
`)
	want := []string{
		"retyped field -ID int +ID int64",
		`tag field -Name string ` + "`json:\"name\"`" + ` +Name string ` + "`json:\"full_name\"`",
		"removed field -Admin bool",
		"added field +Phone string",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("member changes:\n got %q\nwant %q", got, want)
	}
}

func TestDiffMembersRename(t *testing.T) {
	got := memberDiff(t, `package a

type User struct {
	ID   int
	Name string `+"`json:\"name\"`"+`
	Age  int
}
`, `package a

type User struct {
	ID       int
	FullName string `+"`json:\"full_name\"`"+`
	Years    int64
}
`)
	// Name -> FullName keeps type and position: a rename. Age -> Years
	// changes type too, which is too weak a signal to call it one.
	want := []string{
		"tag|renamed field -Name string `json:\"name\"` +FullName string `json:\"full_name\"`",
		"removed field -Age int",
		"added field +Years int64",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("member changes:\n got %q\nwant %q", got, want)
	}
}

func TestDiffMembersEmbedded(t *testing.T) {
	got := memberDiff(t, `package a

type Server struct {
	*Logger
	sync.Mutex
	io.Reader
	addr string
}
`, `package a

type Server struct {
	Logger
	io.Reader
	http.Handler
	addr string
}
`)
	want := []string{
		"retyped embedded field -*Logger +Logger",
		"removed embedded field -sync.Mutex",
		"added embedded field +http.Handler",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("member changes:\n got %q\nwant %q", got, want)
	}
}

func TestDiffMembersInterface(t *testing.T) {
	got := memberDiff(t, `package a

type Store interface {
	io.Closer
	Get(key string) ([]byte, error)
	Put(key string, v []byte) error
}

type Number interface{ ~int | ~int64 }
`, `package a

type Store interface {
	io.Closer
	Get(k string) ([]byte, error)
	Put(ctx context.Context, key string, v []byte) error
	Delete(key string) error
}

type Number interface{ ~int | ~int64 }
`)
	// Renaming Get's parameter is not a change.
	want := []string{
		"retyped method -Put(key string, v []byte) error +Put(ctx context.Context, key string, v []byte) error",
		"added method +Delete(key string) error",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("member changes:\n got %q\nwant %q", got, want)
	}

	got = memberDiff(t, `package a

type Number interface {
	~int | ~int64
	fmt.Stringer
}
`, `package a

type Number interface {
	~int | ~int64 | ~float64
	io.Writer
}
`)
	want = []string{
		"removed type term -~int | ~int64",
		"added type term +~int | ~int64 | ~float64",
		"removed embedded interface -fmt.Stringer",
		"added embedded interface +io.Writer",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("member changes:\n got %q\nwant %q", got, want)
	}
}

func TestDiffMembersReorderAndNonStruct(t *testing.T) {
	// Reordering fields changes the definition but no member.
	cs := diffOne(t, "package a\n\ntype P struct {\n\tX int\n\tY string\n}\n", "package a\n\ntype P struct {\n\tY string\n\tX int\n}\n")
	if len(cs) != 1 || !cs[0].Flags.Has(BodyChanged) || len(cs[0].Members) != 0 {
		t.Errorf("reorder: %+v", cs)
	}
	// A struct that became a named type loses all its fields.
	cs = diffOne(t, "package a\n\ntype P struct{ X int }\n", "package a\n\ntype P int\n")
	if len(cs) != 1 || len(cs[0].Members) != 1 || !cs[0].Members[0].Flags.Has(MemberRemoved) {
		t.Errorf("struct to int: %+v", cs)
	}
}

func TestFieldRefs(t *testing.T) {
	f := mustParse(t, "a.go", `package a

import "example.com/cfg"

type Server struct{ conf Config }

func (s *Server) Run(c cfg.Options) {
	s.conf.Addr = c.Addr
	x := Config{Addr: "a", Port: 1}
	y := &cfg.Options{Addr: "b"}
	s.start()
	_ = []Config{{Port: 2}}
	use(x, y)
}
`)
	var got []string
	for _, r := range symbol(t, f, "Server.Run").FieldRefs {
		got = append(got, fmt.Sprintf("%s %s.%s %d approx=%v", r.Pkg, r.Type, r.Name, r.Count, r.Approximate()))
	}
	want := []string{
		" .Addr 2 approx=true",
		" .Port 1 approx=true",
		" Config.Addr 1 approx=false",
		" Config.Port 1 approx=false",
		" Server.conf 1 approx=false",
		"example.com/cfg Options.Addr 1 approx=false",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("field refs:\n got %q\nwant %q", got, want)
	}
}
