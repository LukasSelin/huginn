package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"huginn/internal/ast"
	"huginn/internal/git"
	"huginn/internal/review"
	"huginn/internal/semdiff"
)

// semanticDiff prints the semantic diff report of one commit.
func semanticDiff(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("semdiff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("C", ".", "repository directory")
	includeGenerated := fs.Bool("include-generated", false, "diff generated files like hand-written ones")
	maxCallers := fs.Int("callers", 5, "list at most `n` callers per change and 3n in the blast radius; 0 for all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("want one revision, got %d", fs.NArg())
	}

	repo, err := git.Open(ctx, *dir)
	if err != nil {
		return err
	}
	c, files, err := repo.Diff(ctx, fs.Arg(0), git.Options{IncludeGenerated: *includeGenerated})
	if err != nil {
		return err
	}
	tree, err := repo.Tree(ctx, c.Hash)
	if err != nil {
		return err
	}

	in := semdiff.Input{Tree: tree.Files, GoMod: tree.GoMod, IncludeGenerated: *includeGenerated}
	for _, fd := range files {
		in.Files = append(in.Files, semdiff.FileDiff{Old: fd.Old, New: fd.New, Added: fd.Added, Removed: fd.Removed})
	}
	for _, err := range c.ParseErrors {
		fmt.Fprintln(stderr, "warning:", err)
	}
	if n := len(tree.ParseErrors); n > 0 {
		fmt.Fprintf(stderr, "warning: %d files in the tree have syntax errors; callers may be missing\n", n)
	}

	p := &printer{w: stdout, maxCallers: *maxCallers}
	p.report(c, semdiff.Build(in))
	return p.err
}

// printer writes a report as plain text, remembering the first write error.
type printer struct {
	w          io.Writer
	maxCallers int
	scoped     bool // the report was limited to a scope
	err        error

	// paths gives what a change does to its function's paths, by the
	// change's index: set for a review, nil for a commit's semantic diff.
	paths func(int) *review.Paths
}

func (p *printer) printf(format string, args ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, args...)
	}
}

func (p *printer) report(c *git.Commit, r *semdiff.Report) {
	subject, _, _ := strings.Cut(strings.TrimSpace(c.Message), "\n")
	p.printf("commit %s\n", c.Hash)
	p.printf("Author: %s <%s>\n", c.Author, c.Email)
	p.printf("Date:   %s\n\n", c.AuthorTime.Format("Mon Jan 2 15:04:05 2006 -0700"))
	p.printf("    %s\n\n", subject)

	p.summary(r)
	p.files(r)
	p.changes(r)
	p.blast("Blast radius", r)
	p.relations(r.Relations)
	p.noise(r)
}

// summary prints the headline and the count of each kind of change.
func (p *printer) summary(r *semdiff.Report) {
	p.printf("%s\n", r.Headline())
	var counts []string
	for _, k := range semdiff.Kinds {
		if n := r.Counts[k]; n > 0 {
			counts = append(counts, fmt.Sprintf("%s %d", k, n))
		}
	}
	if len(counts) > 0 {
		p.printf("%s\n", strings.Join(counts, " · "))
	}
}

// files lists the files with their line counts and kinds of change.
func (p *printer) files(r *semdiff.Report) {
	if len(r.Files) > 0 {
		p.printf("\nFiles\n")
		names := make([]string, len(r.Files))
		width := 0
		for i, f := range r.Files {
			names[i] = f.Path
			if f.OldPath != "" {
				names[i] = f.OldPath + " → " + f.Path
			}
			width = max(width, utf8.RuneCountInString(names[i]))
		}
		for i, f := range r.Files {
			p.printf("  %s  %s", pad(names[i], width), pad(fmt.Sprintf("+%d −%d", f.Added, f.Removed), 10))
			if f.Changes > 0 {
				p.printf("  %s: %s", plural(f.Changes, "symbol"), kinds(f.Kinds))
			}
			p.printf("\n")
		}
	}
}

// changes prints one entry per changed symbol, with its callers.
func (p *printer) changes(r *semdiff.Report) {
	if len(r.Changes) > 0 {
		p.printf("\nChanges\n")
	}
	for i, ch := range r.Changes {
		p.printf("\n  [%s] %s %s\n", kinds(ch.Kinds), ch.Symbol, ch.Name)
		where := fmt.Sprintf("%s:%d", ch.Path, ch.Line)
		if ch.OldPath != "" && (ch.OldPath != ch.Path || ch.OldName != "") {
			where += fmt.Sprintf(" (was %s at %s:%d)", cmpOr(ch.OldName, ch.Name), ch.OldPath, ch.OldLine)
		}
		p.printf("    %s\n", where)
		if ch.OldSignature != "" && ch.OldSignature != ch.NewSignature {
			p.printf("    - %s\n", ch.OldSignature)
		}
		if ch.NewSignature != "" {
			mark := "+"
			if ch.OldSignature == ch.NewSignature {
				mark = " "
			}
			p.printf("    %s %s\n", mark, ch.NewSignature)
		}
		if ch.Summary != "" {
			p.printf("    %s\n", ch.Summary)
		}
		p.flow(i)
		p.members(ch.Members)
		impact := ch.Impact
		if ch.APIChange {
			impact += " · API change"
		}
		p.printf("    %s\n", impact)
		p.callers("      ", ch.Callers, p.maxCallers)
	}
}

// blast prints the blast radius under title: the production code it
// reaches, then how much more lies in tests, which are counted apart since
// they often outnumber it many times. In a scoped report it also says how
// much of the production code lies outside the scope, and lists only those
// packages. The callers are listed production code first.
func (p *printer) blast(title string, r *semdiff.Report) {
	b := r.Blast
	prod, tests := sumCallers(b.Callers, isProduction), sumCallers(b.Callers, isTest)
	p.printf("\n%s\n", title)
	switch {
	case prod.decls == 0 && tests.decls == 0:
		p.printf("  no callers outside the change\n")
	case prod.decls == 0:
		p.printf("  no callers outside tests · %s in tests\n", plural(tests.sites, "call site"))
	default:
		sites := plural(prod.sites, "call site")
		if prod.approx {
			sites += " (approx.)"
		}
		p.printf("  %s in %s, %s", sites, plural(prod.decls, "declaration"), plural(len(prod.pkgs), "package"))
		if tests.decls > 0 {
			p.printf(" · %d more in tests", tests.sites)
		}
		p.printf("\n")
		if !p.scoped {
			for _, pkg := range prod.pkgs {
				p.printf("    %s\n", pkg)
			}
		} else if out := outside(b.Callers); out.decls == 0 {
			p.printf("  none outside the scope\n")
		} else {
			p.printf("  outside the scope: %s in %s, %s\n", plural(out.sites, "call site"),
				plural(out.decls, "declaration"), plural(len(out.pkgs), "package"))
			for _, pkg := range out.pkgs {
				p.printf("    %s\n", pkg)
			}
		}
	}
	if b.ExportedAPIChanged() {
		p.printf("  exported API changed: %s\n", strings.Join(b.APIChanges, ", "))
	}
	p.callers("  ", productionFirst(b.Callers), 3*p.maxCallers)
}

func isProduction(c semdiff.Caller) bool { return !c.Test }
func isTest(c semdiff.Caller) bool       { return c.Test }

// productionFirst returns callers with those in test files moved last,
// each part in its order.
func productionFirst(cs []semdiff.Caller) []semdiff.Caller {
	out := slices.Clone(cs)
	slices.SortStableFunc(out, func(a, b semdiff.Caller) int {
		switch {
		case a.Test == b.Test:
			return 0
		case b.Test:
			return -1
		}
		return 1
	})
	return out
}

// callerSum sums up some of a report's callers.
type callerSum struct {
	sites, decls int
	pkgs         []string // sorted
	approx       bool
}

// sumCallers sums up the callers keep holds for.
func sumCallers(cs []semdiff.Caller, keep func(semdiff.Caller) bool) callerSum {
	var out callerSum
	for _, cl := range cs {
		if !keep(cl) {
			continue
		}
		out.sites += cl.Calls
		out.decls++
		out.approx = out.approx || cl.Approximate
		if !slices.Contains(out.pkgs, cl.Package) {
			out.pkgs = append(out.pkgs, cl.Package)
		}
	}
	slices.Sort(out.pkgs)
	return out
}

// outside sums up the production callers outside a report's scope.
func outside(cs []semdiff.Caller) callerSum {
	return sumCallers(cs, func(c semdiff.Caller) bool { return c.Outside && !c.Test })
}

// noise lists the files whose changes do not matter semantically.
func (p *printer) noise(r *semdiff.Report) {
	if len(r.Noise) > 0 {
		p.printf("\nNoise\n")
		width := 0
		for _, n := range r.Noise {
			width = max(width, utf8.RuneCountInString(n.Path))
		}
		for _, n := range r.Noise {
			p.printf("  %s  %s  %s\n", pad(n.Path, width), pad(fmt.Sprintf("+%d −%d", n.Added, n.Removed), 10), n.Reason)
		}
	}
}

// members lists a type's member changes, one line per side, the notes on
// the last: "+ field UserID int  renamed · breaks callers · 4 field uses",
// then the types implementing the interface that the changes can break.
func (p *printer) members(ms []semdiff.MemberChange) {
	var impls []semdiff.Caller
	for _, m := range ms {
		if len(m.Implementers) > 0 {
			impls = m.Implementers // every member shares the one list
		}
		note := ""
		if notes := memberNotes(m); len(notes) > 0 {
			note = "  " + strings.Join(notes, " · ")
		}
		switch {
		case m.OldDecl == "":
			p.printf("      + %s %s%s\n", m.Member, m.NewDecl, note)
		case m.NewDecl == "":
			p.printf("      - %s %s%s\n", m.Member, m.OldDecl, note)
		default:
			p.printf("      - %s %s\n", m.Member, m.OldDecl)
			p.printf("      + %s %s%s\n", m.Member, m.NewDecl, note)
		}
	}
	p.implementers("        ", impls, p.maxCallers)
}

// memberNotes says what a member change did and what it breaks, as members
// prints them after its line: "renamed", "breaks callers", "4 field uses".
func memberNotes(m semdiff.MemberChange) []string {
	var notes []string
	if m.Flags.Has(ast.MemberRenamed) {
		notes = append(notes, "renamed")
	}
	if m.Flags.Has(ast.MemberRetyped) {
		notes = append(notes, "retyped")
	}
	if m.Flags.Has(ast.MemberTagChanged) {
		notes = append(notes, "tag changed")
	}
	if m.Breaking {
		notes = append(notes, "breaks "+m.Breaks)
	}
	if m.Uses > 0 {
		uses := plural(m.Uses, "field use")
		if m.Approximate {
			uses += " (approx.)"
		}
		notes = append(notes, uses)
	}
	if m.Unkeyed > 0 {
		notes = append(notes, "breaks "+plural(m.Unkeyed, "unkeyed literal"))
	}
	if len(m.Implementers) > 0 {
		notes = append(notes, plural(len(m.Implementers), "implementer")+" (approx.)")
	}
	return notes
}

// implementers lists up to limit types implementing a changed interface,
// all of them if limit is 0.
func (p *printer) implementers(indent string, cs []semdiff.Caller, limit int) {
	for i, cl := range cs {
		if limit > 0 && i == limit {
			p.printf("%s… and %d more\n", indent, len(cs)-limit)
			return
		}
		note := ""
		if cl.Test {
			note = " (test)"
		}
		p.printf("%simplemented by %s  %s:%d%s\n", indent, cl.Name, cl.Path, cl.Line, note)
	}
}

// callers lists up to limit callers, all of them if limit is 0.
func (p *printer) callers(indent string, cs []semdiff.Caller, limit int) {
	for i, cl := range cs {
		if limit > 0 && i == limit {
			p.printf("%s… and %d more\n", indent, len(cs)-limit)
			return
		}
		var notes []string
		if cl.Approximate {
			notes = append(notes, "approx.")
		}
		if cl.Test {
			notes = append(notes, "test")
		}
		if cl.Outside {
			notes = append(notes, "outside")
		}
		note := ""
		if len(notes) > 0 {
			note = " (" + strings.Join(notes, ", ") + ")"
		}
		p.printf("%s%s  %s:%d  ×%d%s\n", indent, cl.Name, cl.Path, cl.Line, cl.Calls, note)
	}
}

func kinds(ks []semdiff.Kind) string {
	s := make([]string, len(ks))
	for i, k := range ks {
		s[i] = k.String()
	}
	return strings.Join(s, ", ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// pad right-pads s with spaces to width runes.
func pad(s string, width int) string {
	return s + strings.Repeat(" ", max(0, width-utf8.RuneCountInString(s)))
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
