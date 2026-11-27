package capslock

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// step writes one element of a path as capslock's JSON has it, called from
// file:line when file is not empty.
func step(name, pkg, file string, line int) string {
	site := ""
	if file != "" {
		site = `, "site": {"filename": ` + quote(file) + `, "line": "` + itoa(line) + `", "column": "2"}`
	}
	return `{"name": ` + quote(name) + `, "package": ` + quote(pkg) + site + `}`
}

func info(capability string, steps ...string) string {
	return `{"capabilityName": "` + capability + `", "path": [` + strings.Join(steps, ", ") + `], "capabilityType": "CAPABILITY_TYPE_DIRECT"}`
}

func quote(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}

func TestParse(t *testing.T) {
	root := t.TempDir()
	in := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	out := `{"capabilityInfo": [` + strings.Join([]string{
		// A method running a program, through another method.
		info("EXEC",
			step("(*m/git.Repo).Tree", "m/git", "", 0),
			step("(*m/git.Repo).run", "m/git", in("git/repo.go"), 40),
			step("(*os/exec.Cmd).Output", "os/exec", in("git/repo.go"), 72)),
		// Its function literal reads a file, by a longer path than the
		// method's own, which is kept.
		info("FILES",
			step("(*m/git.Repo).Tree$1", "m/git", "", 0),
			step("(*m/git.catFile).close", "m/git", in("git/tree.go"), 12),
			step("(*os.File).Close", "os", in("git/cat.go"), 30)),
		info("FILES",
			step("(*m/git.Repo).Tree", "m/git", "", 0),
			step("os.ReadFile", "os", in("git/tree.go"), 50)),
		// An iterator's yield function seems to call net/http's function
		// literal: capslock's call graph, not the program.
		info("NETWORK",
			step("(*m/git.Repo).Tree", "m/git", "", 0),
			step("strings.Lines$1", "strings", in("git/tree.go"), 60),
			step("net/http.ParseCookie$1", "net/http", "/go/src/strings/iter.go", 20)),
		// err.Error() seems to call os/signal's Error method.
		info("MODIFY_SYSTEM_STATE/SIGNALS",
			step("m/review.Build", "m/review", "", 0),
			step("(os/signal.signalError).Error", "os/signal", in("review/review.go"), 9)),
		info("SYSTEM_CALLS",
			step("m/review.Build", "m/review", "", 0),
			step("(internal/syscall/windows.NTStatus).Error", "internal/syscall/windows", in("review/review.go"), 9)),
		// A value receiver, a generic call whose type arguments name a
		// package, and a function literal handed over by the path itself.
		info("FILES",
			step("(m/web.assets).file", "m/web", "", 0),
			step("slices.SortFunc[[]m/ast.Symbol, m/ast.Symbol]", "slices", in("web/assets.go"), 3),
			step("(m/web.assets).file$1", "m/web", "/go/src/slices/sort.go", 9),
			step("os.Stat", "os", in("web/assets.go"), 5)),
		// A method declared through a type alias names the alias's package.
		info("FILES",
			step("m/web.writeJSON", "m/web", "", 0),
			step("(net/http.http2ResponseWriter).Write", "net/http/internal/http2", in("web/json.go"), 8)),
		// json.Encoder writes to an io.Writer, which capslock resolves to
		// *os.File too; the repository's own code writing to a file it
		// opened does write to it, and so may a module calling net.
		info("FILES",
			step("m/cli.Print", "m/cli", "", 0),
			step("(*encoding/json.Encoder).Encode", "encoding/json", in("cli/print.go"), 4),
			step("(*os.File).Write", "os", "/usr/local/go/src/encoding/json/stream.go", 230)),
		info("FILES",
			step("m/cli.Save", "m/cli", "", 0),
			step("(*os.File).Write", "os", in("cli/save.go"), 9)),
		info("NETWORK",
			step("m/db.Open", "m/db", "", 0),
			step("example.com/pgx.Connect", "example.com/pgx", in("db/db.go"), 3),
			step("(*net.Dialer).DialContext", "net", "/home/ada/go/pkg/mod/example.com/pgx@v5.0.0/conn.go", 40)),
		// Older capslock versions give the enum only; init functions are no
		// declared function.
		`{"capability": "CAPABILITY_NETWORK", "path": [` + step("m/web.Serve", "m/web", "", 0) + `, ` + step("net.Listen", "net", in("web/serve.go"), 14) + `]}`,
		info("FILES", step("m/web.init", "m/web", "", 0), step("os.Getwd", "os", in("web/serve.go"), 1)),
	}, ", ") + `], "packageInfo": [{"path": "m/git"}]}`

	res, err := Parse([]byte(out), root)
	if err != nil {
		t.Fatal(err)
	}
	tree := res[Func{"m/git", "(*Repo).Tree"}]
	if len(tree) != 2 || tree[0].Name != "EXEC" || tree[1].Name != "FILES" {
		t.Fatalf("(*Repo).Tree reaches %+v, want EXEC and FILES only", tree)
	}
	exe := tree[0].Path
	want := []Call{
		{Package: "m/git", Name: "(*Repo).Tree"},
		{Package: "m/git", Name: "(*Repo).run", Path: "git/repo.go", Line: 40},
		{Package: "os/exec", Name: "(*Cmd).Output", Path: "git/repo.go", Line: 72},
	}
	if len(exe) != len(want) {
		t.Fatalf("EXEC path = %+v", exe)
	}
	for i := range want {
		if exe[i] != want[i] {
			t.Errorf("EXEC path[%d] = %+v, want %+v", i, exe[i], want[i])
		}
	}
	if p := tree[1].Path; len(p) != 2 || p[1].Name != "ReadFile" {
		t.Errorf("FILES path = %+v, want the shorter one, through os.ReadFile", p)
	}

	if b, ok := res[Func{"m/review", "Build"}]; ok {
		t.Errorf("review.Build reaches %+v, want nothing: both go through Error methods", b)
	}
	file := res[Func{"m/web", "assets.file"}]
	if len(file) != 1 {
		t.Fatalf("assets.file reaches %+v", file)
	}
	names := []string{}
	for _, c := range file[0].Path {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, " "); got != "assets.file SortFunc assets.file.func1 Stat" {
		t.Errorf("assets.file's path = %s", got)
	}
	if file[0].Path[3].Path != "web/assets.go" || file[0].Path[2].Path != "" {
		t.Errorf("sites = %+v, want only those under the root", file[0].Path)
	}
	if w, ok := res[Func{"m/web", "writeJSON"}]; ok {
		t.Errorf("writeJSON reaches %+v, want nothing: an unexported type of net/http", w)
	}
	if s := res[Func{"m/web", "Serve"}]; len(s) != 1 || s[0].Name != "NETWORK" {
		t.Errorf("Serve reaches %+v, want NETWORK", s)
	}
	if p, ok := res[Func{"m/cli", "Print"}]; ok {
		t.Errorf("Print reaches %+v, want nothing: the standard library calls Write through io.Writer", p)
	}
	if s := res[Func{"m/cli", "Save"}]; len(s) != 1 || s[0].Name != "FILES" {
		t.Errorf("Save reaches %+v, want FILES", s)
	}
	if o := res[Func{"m/db", "Open"}]; len(o) != 1 || o[0].Name != "NETWORK" {
		t.Errorf("Open reaches %+v, want NETWORK", o)
	}
	if len(res) != 5 {
		t.Errorf("functions = %v, want Tree, assets.file, Save, Open and Serve: init is none", res)
	}
}

func TestSplit(t *testing.T) {
	for _, c := range []struct{ name, pkg, wantPkg, wantName string }{
		{"(*os/exec.Cmd).Output", "os/exec", "os/exec", "(*Cmd).Output"},
		{"gopkg.in/yaml.v3.Marshal", "gopkg.in/yaml.v3", "gopkg.in/yaml.v3", "Marshal"},
		{"(net/http.http2ResponseWriter).Write", "net/http/internal/http2", "net/http", "http2ResponseWriter.Write"},
		{"slices.Sorted[example.com/m/ast.Kind]", "", "slices", "Sorted[example.com/m/ast.Kind]"},
		{"(*example.com/m.List[int]).Push$1", "example.com/m", "example.com/m", "(*List[int]).Push$1"},
	} {
		if p, n := split(c.name, c.pkg); p != c.wantPkg || n != c.wantName {
			t.Errorf("split(%q, %q) = %q, %q, want %q, %q", c.name, c.pkg, p, n, c.wantPkg, c.wantName)
		}
	}
}

// TestRun runs capslock on a module of two functions, one reading a file
// and one only adding.
func TestRun(t *testing.T) {
	if testing.Short() {
		t.Skip("capslock loads the standard library")
	}
	tool, err := Find()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, src string) {
		p := filepath.Join(dir, filepath.FromSlash(name))
		must(os.MkdirAll(filepath.Dir(p), 0o755))
		must(os.WriteFile(p, []byte(src), 0o644))
	}
	write("go.mod", "module example.com/m\n\ngo 1.22\n")
	write("conf/conf.go", "package conf\n\nimport \"os\"\n\nfunc Load(name string) ([]byte, error) {\n\treturn os.ReadFile(name)\n}\n\nfunc Add(a, b int) int { return a + b }\n")

	res, err := tool.Run(context.Background(), dir, dir, []string{"./conf"})
	if err != nil {
		t.Fatal(err)
	}
	load := res[Func{"example.com/m/conf", "Load"}]
	if len(load) != 1 || load[0].Name != "FILES" {
		t.Fatalf("Load reaches %+v, want FILES", load)
	}
	if p := load[0].Path; len(p) != 2 || p[1].Package != "os" || p[1].Name != "ReadFile" || p[1].Path != "conf/conf.go" || p[1].Line != 6 {
		t.Errorf("Load's path = %+v, want os.ReadFile from conf/conf.go:6", p)
	}
	if add, ok := res[Func{"example.com/m/conf", "Add"}]; ok {
		t.Errorf("Add reaches %+v", add)
	}

	if _, err := tool.Run(context.Background(), dir, dir, []string{"./missing"}); err == nil {
		t.Error("a missing package: want an error")
	}
}
