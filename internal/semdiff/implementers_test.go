package semdiff

import (
	"fmt"
	"strings"
	"testing"

	"huginn/internal/ast"
)

func TestInterfaceImplementers(t *testing.T) {
	oldA := parse(t, "store/store.go", `package store

type Key string

type Store interface {
	Get(k Key) ([]byte, error)
	Put(key string, v []byte) error
}
`)
	newA := parse(t, "store/store.go", `package store

type Key string

type Store interface {
	Get(k Key) ([]byte, error)
	Put(key string, v []byte) error
	Delete(key string) error
}
`)
	// Implements the old Store in the same package, with mixed receivers.
	mem := parse(t, "store/mem.go", `package store

type mem struct{ m map[Key][]byte }

func (s *mem) Get(k Key) ([]byte, error)          { return s.m[k], nil }
func (s mem) Put(key string, value []byte) error { s.m[Key(key)] = value; return nil }
`)
	// Already implements the new Store: updated in the same commit.
	full := parse(t, "store/full.go", `package store

type full struct{}

func (full) Get(Key) ([]byte, error)    { return nil, nil }
func (full) Put(string, []byte) error   { return nil }
func (full) Delete(key string) error    { return nil }
`)
	// Implements it in another package, its methods split across files.
	disk := parse(t, "disk/disk.go", `package disk

import "example.com/app/store"

type Disk struct{ dir string }

func (d *Disk) Get(k store.Key) ([]byte, error) { return nil, nil }

// ReadOnly lacks Put.
type ReadOnly struct{}

func (ReadOnly) Get(k store.Key) ([]byte, error) { return nil, nil }

// Wrong has Put with another signature.
type Wrong struct{}

func (Wrong) Get(k store.Key) ([]byte, error) { return nil, nil }
func (Wrong) Put(key string) error            { return nil }
`)
	diskPut := parse(t, "disk/put.go", `package disk

func (d *Disk) Put(key string, v []byte) error { return nil }
`)
	// Matches by method set, but cannot see store.
	other := parse(t, "other/other.go", `package other

type Key string

type Fake struct{}

func (Fake) Get(k Key) ([]byte, error)       { return nil, nil }
func (Fake) Put(key string, v []byte) error { return nil }
`)
	r := Build(Input{
		Files: []FileDiff{{Old: oldA, New: newA, Added: 1}},
		Tree:  []*ast.File{newA, mem, full, disk, diskPut, other},
		GoMod: goMod,
	})

	st := change(t, r, "store.Store")
	if got := members(st.Members); fmt.Sprint(got) != "[added method Delete breaks implementers]" {
		t.Fatalf("members = %q", got)
	}
	var got []string
	for _, c := range st.Members[0].Implementers {
		if !c.Approximate {
			t.Errorf("%s is not marked approximate", c.Name)
		}
		got = append(got, fmt.Sprintf("%s %s:%d", c.Name, c.Path, c.Line))
	}
	want := []string{"disk.Disk disk/disk.go:5", "store.mem store/mem.go:3"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("implementers:\n got %q\nwant %q", got, want)
	}
	if !strings.Contains(st.Impact, "2 implementers (approx.)") {
		t.Errorf("Impact = %q", st.Impact)
	}
}

func TestNoImplementersSearchForCallerOnlyChange(t *testing.T) {
	oldA := parse(t, "shape/shape.go", "package shape\n\ntype Shape interface {\n\tArea() float64\n\tName() string\n}\n")
	newA := parse(t, "shape/shape.go", "package shape\n\ntype Shape interface {\n\tArea() float64\n}\n")
	sq := parse(t, "shape/square.go", `package shape

type Square struct{ s float64 }

func (q Square) Area() float64 { return q.s * q.s }
func (q Square) Name() string  { return "square" }
`)
	r := Build(Input{Files: []FileDiff{{Old: oldA, New: newA}}, Tree: []*ast.File{newA, sq}, GoMod: goMod})
	c := change(t, r, "shape.Shape")
	if len(c.Members) != 1 || c.Members[0].Implementers != nil {
		t.Errorf("removing a method breaks callers only, got %+v", c.Members)
	}
}
