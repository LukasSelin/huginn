package semdiff

import (
	"fmt"
	"strings"
	"testing"

	"huginn/internal/ast"
)

func members(ms []MemberChange) []string {
	var out []string
	for _, m := range ms {
		s := fmt.Sprintf("%v %s %s", m.Flags, m.Member, m.Name)
		if m.OldName != "" {
			s += " was " + m.OldName
		}
		if m.Breaking {
			s += " breaks " + m.Breaks
		}
		if m.Uses > 0 {
			s += fmt.Sprintf(" uses=%d", m.Uses)
			if m.Approximate {
				s += " approx"
			}
		}
		out = append(out, s)
	}
	return out
}

func TestStructMemberChanges(t *testing.T) {
	oldA := parse(t, "model/user.go", `package model

type User struct {
	ID    int
	Name  string `+"`json:\"name\"`"+`
	Email string
	admin bool
	*Audit
}

type Audit struct{ By string }
`)
	newA := parse(t, "model/user.go", `package model

type User struct {
	ID       int64
	FullName string `+"`json:\"full_name\"`"+`
	admin    int
	Audit
	Phone string
}

type Audit struct{ By string }
`)
	use := parse(t, "api/api.go", `package api

import "example.com/app/model"

func New(id int) model.User { return model.User{ID: int64(id), FullName: "x"} }

func Show(u model.User) string { return u.FullName + u.Phone + u.By }

func Id(u *model.User) int64 { return u.ID }
`)
	r := Build(Input{
		Files: []FileDiff{{Old: oldA, New: newA, Added: 5, Removed: 5}},
		Tree:  []*ast.File{newA, use},
		GoMod: goMod,
	})

	c := change(t, r, "model.User")
	// The tree is after the commit, so the old names of a removed or renamed
	// field have no uses left; the retyped ID still does.
	want := []string{
		"retyped field ID breaks callers uses=2 approx",
		"tag|renamed field FullName was Name breaks callers",
		"removed field Email breaks callers",
		"retyped field admin",
		"retyped embedded field Audit breaks callers",
		"added field Phone",
	}
	if got := members(c.Members); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("members:\n got %q\nwant %q", got, want)
	}
	if !c.APIChange || fmt.Sprint(r.Blast.APIChanges) != "[model.User]" {
		t.Errorf("APIChange=%v Blast=%v", c.APIChange, r.Blast.APIChanges)
	}
	if c.Summary != "+1 field · −1 field · 3 members retyped · 1 member renamed · 1 tag changed" {
		t.Errorf("Summary = %q", c.Summary)
	}
	if c.Impact != "1 call site · 2 field uses (approx.) · exported" {
		t.Errorf("Impact = %q", c.Impact)
	}
	id := c.Members[0]
	if id.OldDecl != "ID int" || id.NewDecl != "ID int64" {
		t.Errorf("ID decls = %q → %q", id.OldDecl, id.NewDecl)
	}
}

func TestFieldUsesBeforeCommit(t *testing.T) {
	// Uses are searched in the tree given, whatever revision it is.
	oldA := parse(t, "model/user.go", "package model\n\ntype User struct {\n\tName string\n\tAge  int\n}\n")
	newA := parse(t, "model/user.go", "package model\n\ntype User struct {\n\tAge int\n}\n")
	use := parse(t, "model/new.go", `package model

func NewUser(n string) *User { return &User{Name: n} }

func (u *User) Hello() string { return "hi " + u.Name }
`)
	r := Build(Input{Files: []FileDiff{{Old: oldA, New: newA}}, Tree: []*ast.File{oldA, use}, GoMod: goMod})
	want := []string{"removed field Name breaks callers uses=2"}
	if got := members(change(t, r, "model.User").Members); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("members:\n got %q\nwant %q", got, want)
	}
}

func TestInterfaceMethodAdded(t *testing.T) {
	oldA := parse(t, "store/store.go", `package store

type Store interface {
	Get(key string) ([]byte, error)
	Put(key string, v []byte) error
	close()
}

type cache interface{ Get(key string) []byte }
`)
	newA := parse(t, "store/store.go", `package store

type Store interface {
	Get(key string) ([]byte, error)
	Put(ctx context.Context, key string, v []byte) error
	Delete(key string) error
}

type cache interface {
	Get(key string) []byte
	Len() int
}
`)
	r := Build(Input{Files: []FileDiff{{Old: oldA, New: newA}}, Tree: []*ast.File{newA}, GoMod: goMod})

	st := change(t, r, "store.Store")
	want := []string{
		"retyped method Put breaks callers and implementers",
		"removed method close",
		"added method Delete breaks implementers",
	}
	if got := members(st.Members); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Store members:\n got %q\nwant %q", got, want)
	}
	if !st.APIChange || st.Summary != "+1 method · −1 method · 1 member retyped" {
		t.Errorf("Store APIChange=%v Summary=%q", st.APIChange, st.Summary)
	}

	// An unexported interface is no API, so nothing outside can break.
	ca := change(t, r, "store.cache")
	if got := members(ca.Members); fmt.Sprint(got) != "[added method Len]" || ca.APIChange {
		t.Errorf("cache members = %q APIChange=%v", got, ca.APIChange)
	}
	if fmt.Sprint(r.Blast.APIChanges) != "[store.Store]" {
		t.Errorf("Blast.APIChanges = %v", r.Blast.APIChanges)
	}
}

func TestOnlyAddedFieldIsNotAPIChange(t *testing.T) {
	oldA := parse(t, "cfg/cfg.go", "package cfg\n\ntype Options struct{ Addr string }\n")
	newA := parse(t, "cfg/cfg.go", "package cfg\n\ntype Options struct {\n\tAddr string `json:\"addr\"`\n\tPort int\n}\n")
	r := Build(Input{Files: []FileDiff{{Old: oldA, New: newA}}, Tree: []*ast.File{newA}, GoMod: goMod})
	c := change(t, r, "cfg.Options")
	if got := members(c.Members); fmt.Sprint(got) != "[tag field Addr added field Port]" || c.APIChange {
		t.Errorf("members = %q APIChange=%v", got, c.APIChange)
	}
}

func TestUnkeyedLiteralsOfChangedStruct(t *testing.T) {
	oldA := parse(t, "geo/geo.go", "package geo\n\ntype Point struct{ X, Y int }\n")
	newA := parse(t, "geo/geo.go", "package geo\n\ntype Point struct {\n\tX, Y int\n\tlabel string\n}\n")
	use := parse(t, "geo/use.go", `package geo

func Origin() Point { return Point{0, 0} }

func Unit() []Point { return []Point{Point{1, 0}, {0, 1}, Point{X: 1, Y: 1}} }
`)
	r := Build(Input{Files: []FileDiff{{Old: oldA, New: newA}}, Tree: []*ast.File{newA, use}, GoMod: goMod})
	c := change(t, r, "geo.Point")
	// Point{0, 0} and Point{1, 0} spell their type; {0, 1} elides it.
	if len(c.Members) != 1 || c.Members[0].Unkeyed != 2 || c.Members[0].Breaking {
		t.Fatalf("members = %+v", c.Members)
	}
	if !strings.Contains(c.Impact, "2 unkeyed literals") {
		t.Errorf("Impact = %q", c.Impact)
	}
}
