package ast

import (
	"fmt"
	"testing"
)

func TestRefs(t *testing.T) {
	f := mustParse(t, "server.go", `package srv

import (
	"fmt"
	"net/url"
	str "strings"

	"example.com/mod/v2"
	_ "embed"
)

type Server struct{ h Handler }

func (s *Server) Handle(name string) error {
	url := name // shadows the import
	if err := s.check(url); err != nil {
		return fmt.Errorf("handle: %w", err)
	}
	s.h.Serve(str.ToUpper(url))
	cfg := Config{Name: name}
	mod.Run(cfg)
	helper(defaultTimeout)
	f := helper
	f(1)
	return nil
}
`)
	if len(f.Imports) != 5 || f.Imports[2] != (Import{Name: "str", Path: "strings"}) {
		t.Errorf("Imports = %+v", f.Imports)
	}

	var got []string
	for _, r := range symbol(t, f, "Server.Handle").Refs {
		got = append(got, fmt.Sprintf("%v %s %s.%s %d/%d approx=%v", r.Kind, r.Pkg, r.Recv, r.Name, r.Calls, r.Count, r.Approximate()))
	}
	want := []string{
		"local  .Config 0/1 approx=false",
		"local  .defaultTimeout 0/1 approx=false",
		"local  .helper 1/2 approx=false",
		"local  .nil 0/2 approx=false",
		"import example.com/mod/v2 .Run 1/1 approx=false",
		"import fmt .Errorf 1/1 approx=false",
		"import strings .ToUpper 1/1 approx=false",
		"method  .Serve 1/1 approx=true",
		"method  Server.check 1/1 approx=false",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("refs:\n got %q\nwant %q", got, want)
	}
	if r := symbol(t, f, "Server.Handle").Refs[0]; r.Line != 20 {
		t.Errorf("Config first seen on line %d, want 20", r.Line)
	}
	if refs := symbol(t, f, "Server").Refs; refs != nil {
		t.Errorf("type has refs %+v", refs)
	}
}

func TestImportName(t *testing.T) {
	for path, want := range map[string]string{
		"fmt":                     "fmt",
		"net/http":                "http",
		"example.com/mod/v2":      "mod",
		"gopkg.in/yaml.v3":        "yaml",
		"github.com/x/go-cmp":     "cmp",
		"github.com/x/kebab-case": "kebab_case",
	} {
		if got := ImportName(path); got != want {
			t.Errorf("ImportName(%q) = %q, want %q", path, got, want)
		}
	}
}
