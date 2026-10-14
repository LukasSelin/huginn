// Command huginn explores a Go repository's history symbol by symbol.
//
//	huginn symbols [-C dir] [-branch rev] [-tests] name
//
// prints the life of every symbol whose qualified name is name, e.g.
// "ParseFile" or "(*Indexer).Visit", newest change first.
//
//	huginn serve [-C dir] [-branch rev] [-tests] [-addr host:port]
//
// indexes the history and serves the symbol timeline and the semantic diff
// of each commit in a browser.
//
//	huginn semdiff [-C dir] [-include-generated] [-callers n] rev
//
// prints the semantic diff of one commit: its changed symbols, their
// callers, the blast radius and the files that changed only in formatting.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"huginn/internal/git"
	"huginn/internal/lineage"
	"huginn/internal/web"
)

const usage = `usage:
  huginn symbols [-C dir] [-branch rev] [-tests] name
  huginn serve [-C dir] [-branch rev] [-tests] [-addr host:port]
  huginn semdiff [-C dir] [-include-generated] [-callers n] rev`

func main() {
	cmds := map[string]func(context.Context, io.Writer, io.Writer, []string) error{
		"symbols": symbols,
		"serve":   serve,
		"semdiff": semanticDiff,
	}
	if len(os.Args) < 2 || cmds[os.Args[1]] == nil {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := cmds[os.Args[1]](ctx, os.Stdout, os.Stderr, os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "huginn:", err)
		os.Exit(1)
	}
}

func symbols(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("symbols", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("C", ".", "repository directory")
	branch := fs.String("branch", "", "revision to walk (default HEAD)")
	tests := fs.Bool("tests", false, "include symbols declared in _test.go files")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("want one symbol name, got %d", fs.NArg())
	}

	repo, err := git.Open(*dir)
	if err != nil {
		return err
	}
	start := time.Now()
	h, err := lineage.Build(ctx, repo, lineage.BuildOptions{Options: git.Options{Branch: *branch}, Tests: *tests})
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "indexed %d live symbols in %v\n", h.Live(), time.Since(start).Round(time.Millisecond))

	found := h.Find(fs.Arg(0))
	if len(found) == 0 {
		return fmt.Errorf("no symbol named %q", fs.Arg(0))
	}
	for i, s := range found {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		printSymbol(stdout, s)
	}
	return nil
}

func printSymbol(w io.Writer, s *lineage.Symbol) {
	cur := s.Latest()
	state := "alive"
	if !s.Alive() {
		state = "deleted"
	}
	fmt.Fprintf(w, "%s  %s:%d  %s  %d changes\n", cur.QualifiedName(), cur.Path, cur.StartLine, state, len(s.Events))
	for i := len(s.Events) - 1; i >= 0; i-- {
		e := s.Events[i]
		fmt.Fprintf(w, "  %s  %.7s  %-16s  %-22s  %s\n", e.Time.Format("2006-01-02"), e.SHA, e.Author, e.Flags, e.Summary())
	}
}

func serve(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("C", ".", "repository directory")
	branch := fs.String("branch", "", "revision to walk (default HEAD)")
	tests := fs.Bool("tests", false, "include symbols declared in _test.go files")
	addr := fs.String("addr", "localhost:7878", "address to listen on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo, err := git.Open(*dir)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}
	srv := web.New(repo, filepath.Base(abs), lineage.BuildOptions{Options: git.Options{Branch: *branch}, Tests: *tests})

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "huginn: serving http://%s\n", ln.Addr())
	go func() {
		start := time.Now()
		if err := srv.Index(ctx); err != nil {
			fmt.Fprintln(stderr, "huginn: indexing:", err)
			return
		}
		fmt.Fprintf(stderr, "huginn: indexed in %v\n", time.Since(start).Round(time.Millisecond))
	}()

	hs := &http.Server{Handler: srv.Handler()}
	go func() {
		<-ctx.Done()
		hs.Close()
	}()
	if err := hs.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}
