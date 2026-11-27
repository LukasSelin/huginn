package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"huginn/internal/affinity"
	"huginn/internal/git"
	"huginn/internal/lineage"
)

// showAffinity prints the affinity analysis of one type: its methods
// against its fields, clustered into blocks, with how they change together,
// who calls them and what their names share. Without a type it lists the
// types that look like they do too much.
func showAffinity(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("affinity", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("C", ".", "repository directory")
	branch := fs.String("branch", "", "revision to walk (default HEAD)")
	tests := fs.Bool("tests", false, "include symbols declared in _test.go files")
	noCache := fs.Bool("no-cache", false, "walk all history instead of reusing .git/huginn")
	months := fs.Int("months", 12, "months of history to read change coupling from, back from the last commit; 0 for all")
	hub := fs.Float64("hub", 0.5, "share of the methods using fields above which a configuration or collaborator field is a hub")
	minCo := fs.Int("min-co", 2, "commits two methods must have changed in together to count as coupled")
	top := fs.Int("n", 20, "candidates to list")
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("want at most one type name, got %d", fs.NArg())
	}

	repo, err := git.Open(ctx, *dir)
	if err != nil {
		return err
	}
	opts, err := buildOptions(ctx, repo, *branch, *tests, *noCache)
	if err != nil {
		return err
	}
	start := time.Now()
	h, err := lineage.Build(ctx, repo, opts)
	if err != nil {
		return err
	}
	an := affinity.New(h, *tests)
	o := affinity.Options{Months: *months, HubShare: *hub, MinCoChanges: *minCo, Top: *top}

	var out any
	if fs.NArg() == 0 {
		out = an.Candidates(o)
	} else {
		id, err := an.Lookup(fs.Arg(0))
		if err != nil {
			return err
		}
		if out, err = an.Report(id, o); err != nil {
			return err
		}
	}
	fmt.Fprintf(stderr, "analyzed %d live symbols in %v%s\n", h.Live(), time.Since(start).Round(time.Millisecond), cacheNote(opts.Cache))

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	w := &printer{w: stdout}
	switch out := out.(type) {
	case []affinity.Candidate:
		printCandidates(w, out, *months)
	case *affinity.Report:
		printAffinity(w, out)
	}
	return w.err
}

func printCandidates(w *printer, cs []affinity.Candidate, months int) {
	if len(cs) == 0 {
		w.printf("no struct types with methods\n")
		return
	}
	window := "all history"
	if months > 0 {
		window = fmt.Sprintf("last %d months", months)
	}
	w.printf("types that may do too much, edits from the %s\n\n", window)
	w.printf("  %6s  %7s  %6s  %6s  %5s  %5s  %s\n", "score", "methods", "fields", "blocks", "lcom4", "edits", "type")
	for _, c := range cs {
		w.printf("  %6.1f  %7d  %6d  %6d  %5d  %5d  %s.%s  %s:%d\n", c.Score, c.Methods, c.Fields, c.Split, c.LCOM4, c.Edits, c.Dir, c.Name, c.Path, c.Line)
	}
}

func printAffinity(w *printer, r *affinity.Report) {
	w.printf("type %s.%s  %s:%d\n", r.Dir, r.Type.Name, r.Type.Path, r.Type.Line)
	window := "all history"
	if r.Months > 0 {
		window = fmt.Sprintf("%d months", r.Months)
	}
	w.printf("%s · %s · LCOM4 %d → %s · %s in %s",
		plural(len(r.Rows), "method"), plural(len(r.Columns), "field"), r.LCOM4, plural(r.Split, "block"),
		plural(r.Coupling.Commits, "commit"), window)
	if r.Coupling.MassEdits > 0 {
		w.printf(" (%s changing over %d methods left out)", plural(r.Coupling.MassEdits, "commit"), r.Coupling.Limit)
	}
	w.printf("\n")
	if r.Unattributed > 0 {
		w.printf("%s on the receiver name no field or method\n", plural(r.Unattributed, "selector"))
	}
	if len(r.Columns) > 0 && len(r.Rows) > 0 {
		w.printf("\n")
		printMatrix(w, r)
	}
	printBlocks(w, r)
	printFindings(w, r)
	printCoupling(w, r)
	printClients(w, r)
	printNames(w, r)
	printSteps(w, r)
}

// printMatrix draws the matrix in display order: a staircase of field
// names, then a line per method with its block, its cells and its calls.
func printMatrix(w *printer, r *affinity.Report) {
	nameWidth := 0
	for _, row := range r.Rows {
		nameWidth = max(nameWidth, utf8.RuneCountInString(row.Method))
	}
	printMatrixHeader(w, r, strings.Repeat(" ", 3+nameWidth+2))
	for _, row := range r.Rows {
		printMatrixRow(w, r, row, nameWidth)
	}
}

// matrixSep returns what goes before column j: a bar between blocks, a
// double one before the hubs, infrastructure and unused fields.
func matrixSep(r *affinity.Report, j int) string {
	if j == 0 {
		return ""
	}
	prev, cur := r.Columns[j-1], r.Columns[j]
	switch {
	case cur.Block < 0 && prev.Block >= 0:
		return "║ "
	case cur.Block != prev.Block && cur.Block >= 0:
		return "│ "
	}
	return ""
}

// printMatrixHeader draws the staircase of field names, one line per
// column, with a dotted rule down from each column before it.
func printMatrixHeader(w *printer, r *affinity.Report, indent string) {
	for k := range r.Columns {
		var line strings.Builder
		line.WriteString(indent)
		for j := range k {
			line.WriteString(matrixSep(r, j))
			line.WriteString("┆  ")
		}
		line.WriteString(matrixSep(r, k))
		line.WriteString(matrixColumnName(r.Columns[k]))
		w.printf("%s\n", strings.TrimRight(line.String(), " "))
	}
}

// matrixColumnName names a column in the header, marking hubs,
// infrastructure and unused fields.
func matrixColumnName(c affinity.Column) string {
	switch {
	case c.Hub:
		return c.Name + " (hub)"
	case c.Class == affinity.Infra || c.Class == affinity.Unused:
		return c.Name + " (" + string(c.Class) + ")"
	}
	return c.Name
}

// printMatrixRow draws one method's line: its block, its cells and its
// calls.
func printMatrixRow(w *printer, r *affinity.Report, row affinity.Row, nameWidth int) {
	var line strings.Builder
	fmt.Fprintf(&line, "%-2s %s  ", rowTag(r, row), pad(row.Method, nameWidth))
	for j, c := range matrixCells(r, row) {
		line.WriteString(matrixSep(r, j) + c + " ")
	}
	if len(row.Calls) > 0 {
		line.WriteString(" → " + matrixCalls(r, row))
	}
	w.printf("%s\n", strings.TrimRight(line.String(), " "))
}

// matrixCells returns a row's cell in each column: RW, W or R where the
// method writes or reads the field, a dot where it does neither.
func matrixCells(r *affinity.Report, row affinity.Row) []string {
	cells := make([]string, len(r.Columns))
	for j := range cells {
		cells[j] = "· "
	}
	for _, c := range row.Cells {
		switch {
		case c.Read && c.Write:
			cells[c.Col] = "RW"
		case c.Write:
			cells[c.Col] = "W "
		default:
			cells[c.Col] = "R "
		}
	}
	return cells
}

// matrixCalls names the methods a row calls, marking those it uses as a
// value, the first four and a count of the rest.
func matrixCalls(r *affinity.Report, row affinity.Row) string {
	var names []string
	for _, c := range row.Calls {
		n := r.Rows[c.Row].Method
		if c.Value {
			n += " (value)"
		}
		names = append(names, n)
	}
	if len(names) > 4 {
		names = append(names[:4], fmt.Sprintf("+%d", len(names)-4))
	}
	return strings.Join(names, ", ")
}

func printBlocks(w *printer, r *affinity.Report) {
	if len(r.Blocks) == 0 {
		return
	}
	w.printf("\nBlocks\n")
	for _, b := range r.Blocks {
		var cols, rows []string
		for _, j := range b.Columns {
			cols = append(cols, r.Columns[j].Name)
		}
		for _, i := range b.Rows {
			rows = append(rows, r.Rows[i].Method)
		}
		if b.Label == string(b.Kind) {
			w.printf("  %s %s\n", b.Letter, b.Kind)
		} else {
			w.printf("  %s %-8s %s\n", b.Letter, b.Kind, b.Label)
		}
		if len(cols) > 0 {
			w.printf("      fields   %s\n", strings.Join(cols, ", "))
		}
		if len(rows) > 0 {
			w.printf("      methods  %s\n", strings.Join(rows, ", "))
		}
		w.printf("      %s\n", b.Reason)
	}
	for _, row := range r.Rows {
		if row.Kind != affinity.Member || len(row.Notes) > 0 {
			w.printf("  %-2s %s: %s", rowTag(r, row), row.Method, row.Reason)
			for _, n := range row.Notes {
				w.printf("; %s", n)
			}
			w.printf("\n")
		}
	}
}

// rowTag names where a row was placed: its block's letter, br for a bridge
// or fn for a free method.
func rowTag(r *affinity.Report, row affinity.Row) string {
	switch row.Kind {
	case affinity.Bridge:
		return "br"
	case affinity.Free:
		return "fn"
	default:
		return r.Blocks[row.Block].Letter
	}
}

func printFindings(w *printer, r *affinity.Report) {
	if len(r.Findings) == 0 {
		return
	}
	w.printf("\nFindings\n")
	for _, f := range r.Findings {
		w.printf("  %-12s %s\n", f.Kind, f.Text)
		w.printf("  %-12s → %s\n", "", f.Advice)
	}
}

func printCoupling(w *printer, r *affinity.Report) {
	c := r.Coupling
	w.printf("\nChange coupling (%s", plural(c.Commits, "commit"))
	if c.MassEdits > 0 {
		w.printf(", %s changing over %d methods left out", plural(c.MassEdits, "commit"), c.Limit)
	}
	w.printf(")\n")
	if c.Commits == 0 {
		w.printf("  no changes in the window\n")
		return
	}
	for _, b := range r.Blocks {
		w.printf("  %s  %s in %s", b.Letter, plural(b.Edits, "edit"), plural(b.Commits, "commit"))
		if b.Cohesion != nil {
			w.printf(" · %.0f%% of its co-changes inside", 100**b.Cohesion)
		}
		w.printf("\n")
		for _, o := range b.Outside {
			w.printf("       with %s  %s  %d (%.0f%%)\n", o.Name, o.Path, o.N, 100*o.Share)
		}
	}
	var strong []affinity.Pair
	for _, p := range c.Pairs {
		if p.Strong {
			strong = append(strong, p)
		}
	}
	if len(strong) == 0 {
		w.printf("  no two methods changed together in %s or more\n", plural(c.Min, "commit"))
		return
	}
	w.printf("  pairs\n")
	for n, p := range strong {
		if n == 10 {
			w.printf("    … and %d more\n", len(strong)-10)
			break
		}
		a, b := rowTag(r, r.Rows[p.A]), rowTag(r, r.Rows[p.B])
		where := "within " + a
		if a != b {
			where = "across " + a + "/" + b
		}
		w.printf("    %3d  %3.0f%%  %s ↔ %s  %s\n", p.N, 100*p.Logical, r.Rows[p.A].Method, r.Rows[p.B].Method, where)
		if p.Text != "" {
			w.printf("              %s\n", p.Text)
		}
	}
}

func printClients(w *printer, r *affinity.Report) {
	cs := r.Clients
	w.printf("\nClients (%d)\n", len(cs.List))
	width := 0
	for _, c := range cs.List {
		width = max(width, utf8.RuneCountInString(c.Name))
	}
	for _, c := range cs.List {
		var uses []string
		for _, u := range c.Uses {
			n := r.Rows[u.Row].Method
			if u.Approximate {
				n += "~"
			}
			uses = append(uses, n)
		}
		w.printf("  %s  %s\n  %s  %s\n", pad(c.Name, width), strings.Join(uses, ", "), pad("", width), c.Text)
	}
	if len(cs.Roles) > 1 {
		w.printf("  roles\n")
		for n, role := range cs.Roles {
			w.printf("    %d. %s\n", n+1, role.Text)
		}
	}
	if len(cs.Uncalled) > 0 {
		w.printf("  uncalled\n")
		for _, u := range cs.Uncalled {
			w.printf("    %s: %s\n", r.Rows[u.Row].Method, u.Why)
		}
	}
	for _, c := range cs.List {
		if c.Interface != "" {
			w.printf("  in %s:\n", c.Name)
			for _, line := range strings.Split(c.Interface, "\n") {
				w.printf("    %s\n", line)
			}
		}
	}
}

func printNames(w *printer, r *affinity.Report) {
	n := r.Names
	if len(n.Words) == 0 && len(n.Flags) == 0 && len(n.Groups) <= 1 {
		return
	}
	w.printf("\nNames\n")
	for k, word := range n.Words {
		if k == 10 {
			w.printf("  … and %d more words\n", len(n.Words)-10)
			break
		}
		var members []string
		for _, j := range word.Cols {
			members = append(members, r.Columns[j].Name)
		}
		for _, i := range word.Rows {
			members = append(members, r.Rows[i].Method)
		}
		w.printf("  %-12s %s\n", word.Word, strings.Join(members, ", "))
	}
	for _, f := range n.Flags {
		w.printf("  flag %s: %s\n", r.Columns[f.Col].Name, f.Why)
	}
	if len(n.Groups) > 1 {
		for _, g := range n.Groups {
			var cols []string
			for _, j := range g.Cols {
				cols = append(cols, r.Columns[j].Name)
			}
			state := "within one block"
			if !g.Agrees {
				state = "across blocks"
			}
			w.printf("  declared together: %s (%s)\n", strings.Join(cols, ", "), state)
		}
	}
}

func printSteps(w *printer, r *affinity.Report) {
	if len(r.Steps) > 0 {
		w.printf("\nSuggested order\n")
		for n, s := range r.Steps {
			w.printf("  %d. %s\n", n+1, s.Text)
			if len(s.Signals) > 0 {
				w.printf("     %s\n", strings.Join(s.Signals, " · "))
			}
		}
	}
	if len(r.Notes) > 0 {
		w.printf("\nWhere the signals disagree\n")
		for _, n := range r.Notes {
			w.printf("  - %s\n", n.Text)
		}
	}
}
