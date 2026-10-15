package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"huginn/internal/codemap"
	"huginn/internal/git"
	"huginn/internal/journey"
	"huginn/internal/lineage"
	"huginn/internal/story"
)

// tell prints the story of a repository's history, chapter by chapter, as
// the journey page tells it.
func tell(ctx context.Context, stdout, stderr io.Writer, args []string) error {
	fs := flag.NewFlagSet("story", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("C", ".", "repository directory")
	branch := fs.String("branch", "", "revision to walk (default HEAD)")
	tests := fs.Bool("tests", false, "include symbols declared in _test.go files")
	noCache := fs.Bool("no-cache", false, "walk all history instead of reusing .git/huginn")
	moments := fs.Bool("moments", false, "list each chapter's key moments")
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
	opts, err := buildOptions(ctx, repo, *branch, *tests, *noCache)
	if err != nil {
		return err
	}
	start := time.Now()
	h, err := lineage.Build(ctx, repo, opts)
	if err != nil {
		return err
	}
	tags, err := repo.Tags(ctx, git.Options{Branch: *branch})
	if err != nil {
		return err
	}
	j := journey.Build(h, codemap.Build(h), journey.Commit, journey.Options{Tests: *tests})
	st, err := story.Build(ctx, h, j, story.FromTags(tags), story.Templates{})
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "told %d commits and %d releases in %v%s\n", h.Revisions(), len(st.Releases), time.Since(start).Round(time.Millisecond), cacheNote(opts.Cache))

	w := &printer{w: stdout}
	for i, c := range st.Chapters {
		if i > 0 {
			w.printf("\n")
		}
		w.printf("%d. %s\n", c.Number, c.Title)
		w.printf("   %s – %s · revisions %d–%d · %s\n", c.From.Format("2006-01-02"), c.To.Format("2006-01-02"), c.FirstRev, c.LastRev, plural(c.Commits, "commit"))
		w.printf("%s\n", wrap(c.Text, 76, "   "))
		if *moments {
			for _, m := range c.Moments {
				w.printf("   · %s %.7s %-11s %s\n", m.Time.Format("2006-01-02"), m.SHA, m.Kind, m.Text)
			}
		}
	}
	return w.err
}

// wrap breaks text into lines of at most width bytes where it can, each
// starting with indent.
func wrap(text string, width int, indent string) string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && len(line)+1+len(word) > width {
			lines = append(lines, indent+line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		lines = append(lines, indent+line)
	}
	return strings.Join(lines, "\n")
}
