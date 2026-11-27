package cohesion

import (
	"cmp"
	"slices"
	"strings"
	"unicode"
)

// words splits an identifier into lower-case words: "HTTPServer" into
// "http" and "server", "billing_name" into "billing" and "name".
func words(name string) []string {
	rs := []rune(name)
	var out []string
	start := 0
	for i := 1; i <= len(rs); i++ {
		boundary := i == len(rs) || rs[i] == '_' || rs[i-1] == '_' ||
			unicode.IsUpper(rs[i]) && (unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1]) ||
				unicode.IsUpper(rs[i-1]) && i+1 < len(rs) && unicode.IsLower(rs[i+1]))
		if !boundary {
			continue
		}
		if w := strings.ToLower(strings.Trim(string(rs[start:i]), "_")); w != "" {
			out = append(out, w)
		}
		start = i
	}
	return out
}

// typeWords returns the words of a field type's package and name:
// "*slog.Logger" gives "slog" and "logger".
func typeWords(typ string) []string {
	t := strings.TrimLeft(typ, "*[]")
	if strings.HasPrefix(t, "func(") || strings.HasPrefix(t, "map[") || strings.HasPrefix(t, "chan ") {
		return nil
	}
	if i := strings.IndexByte(t, '['); i > 0 {
		t = t[:i] // type arguments
	}
	var out []string
	for _, part := range strings.Split(t, ".") {
		out = append(out, words(part)...)
	}
	return out
}

func anyWord(ws []string, set map[string]bool) bool {
	return slices.ContainsFunc(ws, func(w string) bool { return set[w] })
}

var infrastructureWords = set("log", "logger", "slog", "zap", "zerolog", "logrus", "metrics", "metric", "meter", "tracer", "telemetry", "statsd", "prometheus")

var collaboratorWords = set("client", "service", "svc", "repo", "repository", "store", "storage", "db", "database",
	"conn", "connection", "sender", "mailer", "smtp", "gateway", "api", "cache", "queue", "publisher", "producer",
	"consumer", "bus", "provider", "notifier", "fetcher", "transport", "dialer", "pool", "tmpl", "template", "renderer", "writer", "reader")

func set(ws ...string) map[string]bool {
	m := map[string]bool{}
	for _, w := range ws {
		m[w] = true
	}
	return m
}

func isInfrastructure(name, typ string) bool {
	return anyWord(words(name), infrastructureWords) || anyWord(typeWords(typ), infrastructureWords)
}

// isCollaborator guesses that a field holds an I/O handle or a service: a
// function, or something named like a client, store or connection.
func isCollaborator(name, typ string) bool {
	return strings.HasPrefix(strings.TrimLeft(typ, "*"), "func(") ||
		anyWord(words(name), collaboratorWords) || anyWord(typeWords(typ), collaboratorWords)
}

// stopWords say little about what belongs together.
var stopWords = set("get", "set", "new", "with", "from", "for", "and", "the", "all", "has", "not", "can",
	"must", "try", "make", "init", "into", "its")

// modeWords name fields whose value picks a behavior or a state.
var modeWords = set("kind", "mode", "status", "state", "stage", "phase", "typ")

// naming finds words several members share, and fields that look like
// mode flags or states: each value may stand for a type of its own.
func naming(fields []Field, methods []Method) ([]Word, []string) {
	var names []string
	for _, f := range fields {
		names = append(names, f.Name)
	}
	for _, m := range methods {
		names = append(names, m.Name)
	}
	by := map[string][]string{}
	for _, n := range names {
		var seen []string
		for _, w := range words(n) {
			if len(w) < 3 || stopWords[w] || slices.Contains(seen, w) {
				continue
			}
			seen = append(seen, w)
			by[w] = append(by[w], n)
		}
	}
	var out []Word
	for w, ms := range by {
		// A word most members share, as the type's own name often is,
		// groups nothing.
		if len(ms) >= 3 && 5*len(ms) <= 3*len(names) {
			out = append(out, Word{Word: w, Members: ms})
		}
	}
	slices.SortFunc(out, func(a, b Word) int {
		return cmp.Or(cmp.Compare(len(b.Members), len(a.Members)), cmp.Compare(a.Word, b.Word))
	})
	if len(out) > maxWords {
		out = out[:maxWords]
	}

	var flags []string
	for _, f := range fields {
		ws := words(f.Name)
		if f.Type == "bool" || anyWord(ws, modeWords) {
			flags = append(flags, f.Name)
		}
	}
	return out, flags
}

const maxWords = 8

// implicitMethods are called through interfaces the standard library or
// the runtime call, so having no callers in the code says little.
var implicitMethods = set("String", "GoString", "Error", "Format", "Unwrap", "Is", "As",
	"MarshalJSON", "UnmarshalJSON", "MarshalText", "UnmarshalText", "MarshalBinary", "UnmarshalBinary",
	"MarshalYAML", "UnmarshalYAML", "ServeHTTP", "Len", "Less", "Swap", "Read", "Write", "Close", "Seek",
	"ReadFrom", "WriteTo", "Scan", "Value", "LogValue")

// clients lists the packages using the exported methods, and groups them
// into roles: clients connected through the methods they use, once the
// methods most of them use are set aside.
func (a *analysis) clients(in Input) ([]ClientRow, []Role, []string) {
	rows := map[string]*ClientRow{}
	users := map[string][]string{} // method → client packages
	var unused []string
	for i, m := range a.methods {
		if !m.Exported {
			continue
		}
		used := false
		for _, c := range in.Clients(a.syms[i]) {
			if c.Test {
				continue
			}
			used = true
			row := rows[c.Package]
			if row == nil {
				row = &ClientRow{Package: c.Package}
				rows[c.Package] = row
			}
			if !slices.Contains(row.Methods, m.Name) {
				row.Methods = append(row.Methods, m.Name)
			}
			row.Calls += c.Calls
			if !slices.Contains(users[m.Name], c.Package) {
				users[m.Name] = append(users[m.Name], c.Package)
			}
		}
		if !used && !implicitMethods[m.Name] && !in.Interfaces[m.Name] {
			unused = append(unused, m.Name)
		}
	}
	var out []ClientRow
	for _, r := range rows {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(x, y ClientRow) int { return cmp.Compare(x.Package, y.Package) })
	if len(out) == 0 {
		return nil, nil, unused
	}

	idx := map[string]int{}
	for i, r := range out {
		idx[r.Package] = i
	}
	uf := newUnion(len(out))
	hub := func(m string) bool { n := len(users[m]); return len(out) >= 4 && n >= 3 && 2*n > len(out) }
	for m, ps := range users {
		if hub(m) {
			continue
		}
		for _, p := range ps[1:] {
			uf.join(idx[ps[0]], idx[p])
		}
	}
	byRoot := map[int]*Role{}
	var roles []*Role
	for i, r := range out {
		root := uf.find(i)
		role := byRoot[root]
		if role == nil {
			role = &Role{}
			byRoot[root] = role
			roles = append(roles, role)
		}
		role.Clients = append(role.Clients, r.Package)
		for _, m := range r.Methods {
			if !slices.Contains(role.Methods, m) {
				role.Methods = append(role.Methods, m)
			}
		}
	}
	var res []Role
	for _, role := range roles {
		slices.SortFunc(role.Methods, func(x, y string) int { return cmp.Compare(a.method[x], a.method[y]) })
		for _, m := range role.Methods {
			if b := a.methods[a.method[m]].Block; b != "" && !slices.Contains(role.Blocks, b) {
				role.Blocks = append(role.Blocks, b)
			}
		}
		slices.Sort(role.Blocks)
		res = append(res, *role)
	}
	slices.SortStableFunc(res, func(x, y Role) int { return cmp.Compare(len(y.Clients), len(x.Clients)) })
	return out, res, unused
}

// Change coupling counts only methods changed at least minChanges times,
// and pairs changed together at least minTogether times and in at least
// half the changes of the one changed less.
const (
	minChanges  = 3
	minTogether = 2
	maxCoupled  = 20
)

func (a *analysis) coupling(changes map[string][]string) []Coupling {
	var out []Coupling
	for i, x := range a.methods {
		cx := changes[x.Name]
		if len(cx) < minChanges {
			continue
		}
		for _, y := range a.methods[i+1:] {
			cy := changes[y.Name]
			if len(cy) < minChanges {
				continue
			}
			together := 0
			for _, sha := range cx {
				if slices.Contains(cy, sha) {
					together++
				}
			}
			if together < minTogether || 2*together < min(len(cx), len(cy)) {
				continue
			}
			out = append(out, Coupling{
				A: x.Name, B: y.Name, Together: together, AChanges: len(cx), BChanges: len(cy),
				Across: x.Block != "" && y.Block != "" && x.Block != y.Block,
			})
		}
	}
	slices.SortStableFunc(out, func(p, q Coupling) int {
		across := func(c Coupling) int {
			if c.Across {
				return 0
			}
			return 1
		}
		return cmp.Or(cmp.Compare(across(p), across(q)), cmp.Compare(q.Together, p.Together))
	})
	if len(out) > maxCoupled {
		out = out[:maxCoupled]
	}
	return out
}
