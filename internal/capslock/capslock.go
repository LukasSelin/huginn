// Package capslock runs Google's capslock (github.com/google/capslock) on a
// module's packages and reads what it reports: for every function in them,
// the capabilities its calls can reach, such as the network, files or other
// processes, each with one call path that gets there.
//
// capslock's call graph is generous where functions are passed as values,
// so paths that could not run as reported are left out; see plausible.
package capslock

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Tool is the capslock program.
type Tool struct {
	exe string
}

// ErrNotFound says that capslock is not on PATH, and how to install it.
var ErrNotFound = errors.New("capslock is not on PATH; install it with go install github.com/google/capslock/cmd/capslock@latest")

// Find returns the capslock program on PATH, or ErrNotFound.
func Find() (*Tool, error) {
	exe, err := exec.LookPath("capslock")
	if err != nil {
		return nil, ErrNotFound
	}
	return &Tool{exe: exe}, nil
}

// waitDelay bounds how long Wait keeps reading capslock's output once it
// was killed, as for git (see internal/git).
const waitDelay = 5 * time.Second

// Run analyses the packages that patterns ("./internal/git", ".") match in
// the module at dir and returns what their functions reach, with the call
// sites in files under root given relative to it.
func (t *Tool) Run(ctx context.Context, dir, root string, patterns []string) (Result, error) {
	cmd := exec.CommandContext(ctx, t.exe, "-packages", strings.Join(patterns, ","), "-output", "json", "-force_local_module")
	cmd.Dir = dir
	cmd.WaitDelay = waitDelay
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if msg := lastLines(stderr.String(), 6); msg != "" {
			return nil, fmt.Errorf("capslock: %w: %s", err, msg)
		}
		return nil, fmt.Errorf("capslock: %w", err)
	}
	return Parse(out, root)
}

// lastLines returns the last n non-empty lines of s, joined by "; ".
func lastLines(s string, n int) string {
	lines := slices.DeleteFunc(strings.Split(strings.TrimSpace(s), "\n"), func(l string) bool { return strings.TrimSpace(l) == "" })
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return strings.Join(lines, "; ")
}

// Func names a declared function or method: its package's import path and
// its name as Go writes it within the package, "Build", "(*Repo).Tree" or
// "Point.String". The function literals in its body count as part of it.
type Func struct {
	Package string
	Name    string
}

// Call is one step of a call path.
type Call struct {
	Package string // import path
	Name    string // within the package: "(*Cmd).Output"; a function literal is "Tree.func1"

	// Path and Line locate the call, Path relative to the root and
	// slash-separated. Both are empty for the first step and for calls
	// made outside the root, as in the standard library.
	Path string
	Line int
}

// Capability is one capability a function reaches, with one call path
// there: from the function itself to the function capslock finds the
// capability in.
type Capability struct {
	Name string // capslock's name: "NETWORK", "FILES", "EXEC", "MODIFY_SYSTEM_STATE/ENV", ...
	Path []Call
}

// Result is what capslock found, by function. Each function's
// capabilities are sorted by name, one path each, the shortest found.
type Result map[Func][]Capability

// report is the part of capslock's JSON output that Parse reads.
type report struct {
	CapabilityInfo []capabilityInfo `json:"capabilityInfo"`
}

// capabilityInfo is one capability capslock found, with the path to it.
type capabilityInfo struct {
	CapabilityName string `json:"capabilityName"`
	Capability     string `json:"capability"` // older versions: "CAPABILITY_NETWORK"
	Path           []struct {
		Name    string `json:"name"`
		Package string `json:"package"`
		Site    *struct {
			Filename string  `json:"filename"`
			Line     jsonInt `json:"line"`
		} `json:"site"`
	} `json:"path"`
}

// jsonInt reads an integer written as a number or, as protojson writes
// 64-bit integers, as a string.
type jsonInt int

func (n *jsonInt) UnmarshalJSON(b []byte) error {
	i, err := strconv.Atoi(strings.Trim(string(b), `"`))
	*n = jsonInt(i)
	return err
}

// Parse reads capslock's JSON output (-output json), keeping the call sites
// of files under root relative to it. A function literal's capabilities
// are its declaring function's, and paths that could not run as reported
// are left out.
func Parse(data []byte, root string) (Result, error) {
	var rep report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("capslock output: %w", err)
	}
	if root != "" {
		var err error
		if root, err = filepath.Abs(root); err != nil {
			return nil, err
		}
	}
	out := Result{}
	for _, info := range rep.CapabilityInfo {
		if len(info.Path) == 0 {
			continue
		}
		path, std := callPath(info, root)
		fn, ok := declared(path[0])
		if !ok || !plausible(path, std) {
			continue
		}
		out.keep(fn, Capability{Name: cmp.Or(info.CapabilityName, strings.TrimPrefix(info.Capability, "CAPABILITY_")), Path: displayed(path)})
	}
	for _, caps := range out {
		slices.SortFunc(caps, func(a, b Capability) int { return strings.Compare(a.Name, b.Name) })
	}
	return out, nil
}

// callPath reads the path to a capability capslock found, with the call
// sites in files under root relative to it, and which of its calls the
// standard library makes.
func callPath(info capabilityInfo, root string) (path []Call, std []bool) {
	path = make([]Call, len(info.Path))
	std = make([]bool, len(info.Path)) // called from the standard library
	for i, p := range info.Path {
		pkg, name := split(p.Name, p.Package)
		path[i] = Call{Package: pkg, Name: name}
		s := p.Site
		if s == nil || i == 0 {
			continue
		}
		if rel, ok := under(root, s.Filename); ok {
			path[i].Path, path[i].Line = rel, int(s.Line)
		} else {
			std[i] = root != "" && s.Filename != "" && !inModuleCache(s.Filename)
		}
	}
	return path, std
}

// keep adds the capability c of fn, keeping the shorter path of two to the
// same capability.
func (r Result) keep(fn Func, c Capability) {
	caps := r[fn]
	if i := slices.IndexFunc(caps, func(x Capability) bool { return x.Name == c.Name }); i < 0 {
		r[fn] = append(caps, c)
	} else if len(c.Path) < len(caps[i].Path) {
		caps[i] = c
	}
}

// inModuleCache reports whether a file is in Go's module cache, whose
// directories are named module@version, rather than in the standard
// library.
func inModuleCache(file string) bool {
	return strings.Contains(filepath.ToSlash(file), "@v")
}

// under returns file relative to root, slash-separated, if it lies under
// it.
func under(root, file string) (string, bool) {
	if root == "" || file == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// split splits a function's name as capslock writes it,
// "(*example.com/m/git.Repo).Tree$1" or "os/exec.Command", into its
// package's import path and its name within the package, "(*Repo).Tree$1"
// or "Command". pkg is the package capslock gives, which usually starts the
// name; when it does not, as for a method declared through a type alias,
// the name says what package it is in.
func split(name, pkg string) (string, string) {
	recv, ptr := "", false
	rest, isMethod := strings.CutPrefix(name, "(")
	if isMethod {
		rest, ptr = strings.CutPrefix(rest, "*")
	}
	p, after, ok := cutPackage(rest, pkg)
	if !ok {
		return pkg, name
	}
	if isMethod {
		i := strings.Index(after, ").")
		if i < 0 {
			return pkg, name
		}
		recv, after = after[:i], after[i+2:]
		if ptr {
			return p, "(*" + recv + ")." + after
		}
		return p, recv + "." + after
	}
	return p, after
}

// cutPackage cuts the package's import path off the front of s, "pkg.Rest",
// preferring pkg and otherwise taking the path up to the first dot after
// its last slash.
func cutPackage(s, pkg string) (string, string, bool) {
	if pkg != "" {
		if rest, ok := strings.CutPrefix(s, pkg+"."); ok {
			return pkg, rest, true
		}
	}
	end := len(s)
	if i := strings.IndexAny(s, "[$("); i >= 0 {
		end = i
	}
	slash := strings.LastIndex(s[:end], "/")
	dot := strings.Index(s[slash+1:], ".")
	if dot < 0 {
		return "", "", false
	}
	return s[:slash+1+dot], s[slash+1+dot+1:], true
}

// parts breaks a name within its package into its receiver type, if any,
// the function's name and what capslock appends to it: "$1" for a function
// literal, "$bound" or "$thunk" for a method value or expression, or "#2"
// for the second init function. Type arguments are dropped.
func parts(name string) (recv, fn, suffix string) {
	name = dropBrackets(name)
	if r, rest, ok := strings.Cut(name, ")."); ok && strings.HasPrefix(name, "(") {
		recv, name = strings.TrimPrefix(strings.TrimPrefix(r, "("), "*"), rest
	} else if r, rest, ok := strings.Cut(name, "."); ok {
		recv, name = r, rest
	}
	fn = name
	if i := strings.IndexAny(name, "$#"); i >= 0 {
		fn, suffix = name[:i], name[i:]
	}
	return recv, fn, suffix
}

// declared returns the declared function a path starts in: a function
// literal's declaring function, a generic function for its instances. A
// package's initialization is no declared function.
func declared(c Call) (Func, bool) {
	recv, fn, _ := parts(c.Name)
	if fn == "" || fn == "init" {
		return Func{}, false
	}
	switch {
	case recv == "":
		return Func{c.Package, fn}, true
	case strings.HasPrefix(c.Name, "(*"):
		return Func{c.Package, "(*" + recv + ")." + fn}, true
	default:
		return Func{c.Package, recv + "." + fn}, true
	}
}

// displayed renames the steps as Go names them in stack traces: function
// literals end in ".func1" rather than "$1", and type arguments, method
// value wrappers and the numbers of init functions are dropped.
func displayed(path []Call) []Call {
	for i, c := range path {
		recv, fn, suffix := parts(c.Name)
		var b strings.Builder
		switch {
		case recv == "":
		case strings.HasPrefix(c.Name, "(*"):
			b.WriteString("(*" + recv + ").")
		default:
			b.WriteString(recv + ".")
		}
		b.WriteString(fn)
		for _, lit := range literals(suffix) {
			b.WriteString(".func" + lit)
		}
		path[i].Name = b.String()
	}
	return path
}

// literals returns the numbers of the nested function literals a suffix
// from parts names: "$1$2" is the second literal inside the first.
func literals(suffix string) []string {
	var out []string
	for _, s := range strings.Split(suffix, "$")[1:] {
		if s != "" && strings.Trim(s, "0123456789") == "" {
			out = append(out, s)
		}
	}
	return out
}

// dropBrackets removes the bracketed type arguments from s.
func dropBrackets(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '[':
			depth++
		case r == ']' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// plausible reports whether a path could run as capslock reports it.
//
// capslock resolves a call through a function value or an interface to
// every function of the right type, wherever it is declared: an
// iterator's yield function may then seem to call a function literal of
// net/http, err.Error() the Error method of any error, and a json.Encoder
// writing to an io.Writer (*os.File).Write. So a step into a function that
// only its own package can name, a function literal, an unexported
// function or a method of an unexported type, is believed only when the
// path has already been in that package, which handed the function over;
// and so is an Error or String method, which the code calling it rarely
// names, and any method the standard library calls (std, by step), which
// it mostly calls through interfaces. A path with any other such step is
// left out.
func plausible(path []Call, std []bool) bool {
	been := map[string]bool{path[0].Package: true}
	for i, c := range path[1:] {
		if !been[c.Package] && (private(c.Name) || (std[i+1] && method(c.Name))) {
			return false
		}
		been[c.Package] = true
	}
	return true
}

// method reports whether a name within its package is a method's.
func method(name string) bool {
	recv, _, _ := parts(name)
	return recv != ""
}

// private reports whether only a function's own package can call it by
// name, or it is a method that capslock reaches through an interface
// mostly.
func private(name string) bool {
	recv, fn, suffix := parts(name)
	if len(literals(suffix)) > 0 {
		return true
	}
	if !exported(fn) || (recv != "" && !exported(recv)) {
		return true
	}
	return recv != "" && (fn == "Error" || fn == "String")
}

func exported(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}
