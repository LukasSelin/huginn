package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"huginn/internal/moves"
)

// suggestMoves prints the functions in the working tree that only one other
// package refers to, with the helpers that would move along.
func suggestMoves(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("moves", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("C", ".", "module directory")
	blocked := fs.Bool("blocked", false, "also list moves that would not compile as they stand, and why")
	if err := fs.Parse(args); err != nil {
		return err
	}
	patterns := fs.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}

	start := time.Now()
	g, err := moves.Load(ctx, abs, patterns...)
	if err != nil {
		return err
	}
	found := moves.Find(g)
	clean := 0
	for _, m := range found {
		if len(m.Blocked) == 0 {
			clean++
		}
	}
	fmt.Fprintf(stderr, "checked %d packages in %v: %d clean moves, %d blocked\n", len(g.Pkgs), time.Since(start).Round(time.Millisecond), clean, len(found)-clean)

	rel := func(path string) string {
		if r, err := filepath.Rel(abs, path); err == nil {
			return filepath.ToSlash(r)
		}
		return path
	}
	w := &printer{w: stdout}
	first := true
	for _, m := range found {
		if len(m.Blocked) > 0 && !*blocked {
			continue
		}
		if !first {
			w.printf("\n")
		}
		first = false
		pos := g.Pos(m.Func)
		w.printf("%s.%s  %s:%d\n", m.Func.Pkg.PkgPath, m.Func.Name(), rel(pos.Filename), pos.Line)
		users := make([]string, len(m.Users))
		for i, u := range m.Users {
			users[i] = u.Name()
		}
		w.printf("  -> %s, used by %s\n", m.To.PkgPath, strings.Join(users, ", "))
		for _, x := range m.With {
			p := g.Pos(x)
			w.printf("  with %s  %s:%d\n", x.Name(), rel(p.Filename), p.Line)
		}
		for _, f := range m.TestFiles {
			w.printf("  named in %s\n", rel(f))
		}
		for _, why := range m.Blocked {
			w.printf("  blocked: %s\n", why)
		}
	}
	return w.err
}
