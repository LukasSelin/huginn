package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"huginn/internal/git"
	"huginn/internal/lineage"
	"huginn/internal/silos"
)

// listSilos prints the knowledge silos of the live code and the pairs
// coupled across knowledge boundaries, or, with -leaves, what the named
// authors would leave behind.
func listSilos(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("silos", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("C", ".", "repository directory")
	branch := fs.String("branch", "", "revision to walk (default HEAD)")
	noCache := fs.Bool("no-cache", false, "walk all history instead of reusing .git/huginn")
	leaves := fs.String("leaves", "", "comma-separated `authors` to treat as gone: list what they leave orphaned")
	top := fs.Int("n", 20, "entries per list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}

	repo, err := git.Open(*dir)
	if err != nil {
		return err
	}
	opts, err := buildOptions(ctx, repo, *branch, false, *noCache)
	if err != nil {
		return err
	}
	start := time.Now()
	h, err := lineage.Build(ctx, repo, opts)
	if err != nil {
		return err
	}
	rep := silos.Build(h, silos.Options{Top: *top})
	fmt.Fprintf(stderr, "analyzed %d live symbols in %v%s\n", h.Live(), time.Since(start).Round(time.Millisecond), cacheNote(opts.Cache))

	w := &printer{w: stdout}
	if *leaves != "" {
		printDeparture(w, rep.Leave(strings.Split(*leaves, ",")...))
		return w.err
	}
	w.printf("as of %s · %s", rep.Now.Format("2006-01-02"), plural(len(rep.Authors), "author"))
	if len(rep.Departed) > 0 {
		w.printf(" · departed: %s", strings.Join(rep.Departed, ", "))
	}
	w.printf("\n")
	printRisks(w, "Package silos", rep.Packages, rep.PackageSilos)
	printRisks(w, "Symbol silos", rep.Symbols, rep.SymbolSilos)
	printPairs(w, "Packages coupled across knowledge boundaries", rep.PackagePairs)
	printPairs(w, "Symbols coupled across knowledge boundaries", rep.SymbolPairs)
	return w.err
}

func printRisks(w *printer, title string, rs []silos.Risk, total int) {
	w.printf("\n%s (%d of %d)\n", title, len(rs), total)
	for _, r := range rs {
		w.printf("  %6.1f  %s\n", r.Score, where(r.Unit))
		w.printf("          %s\n", strings.Join(r.Reasons, " · "))
	}
}

func printPairs(w *printer, title string, ps []silos.Pair) {
	w.printf("\n%s (%d)\n", title, len(ps))
	for _, p := range ps {
		w.printf("  %6.1f  %s ↔ %s\n", p.Score, p.A.Name, p.B.Name)
		w.printf("          %s\n", strings.Join(p.Reasons, " · "))
	}
}

func printDeparture(w *printer, d *silos.Departure) {
	if len(d.Unknown) > 0 {
		w.printf("no author called %s\n", strings.Join(d.Unknown, ", "))
	}
	if len(d.Leaving) == 0 {
		return
	}
	w.printf("if %s left, %.0f%% of what is known of the live code would go\n", strings.Join(d.Leaving, ", "), 100*d.Share)
	w.printf("orphaned: %s, %s · resting on one person: %s, %s\n",
		plural(d.OrphanedPackages, "package"), plural(d.OrphanedSymbols, "symbol"),
		plural(d.SinglePackages, "package"), plural(d.SingleSymbols, "symbol"))
	for _, l := range [][]silos.Loss{d.Packages, d.Symbols} {
		w.printf("\n")
		for _, x := range l {
			state := "single"
			if x.Orphaned {
				state = "orphaned"
			}
			w.printf("  %6.1f  %-8s  %s\n", x.Score, state, where(x.Unit))
			w.printf("          %s\n", strings.Join(x.Reasons, " · "))
		}
	}
}

// where names a unit: a package by its directory, a symbol with its file.
func where(u silos.Unit) string {
	if u.ID == 0 {
		return u.Name
	}
	return u.Name + "  " + u.Path
}
