package cohesion

import (
	"fmt"
	"slices"
	"strings"
)

// Note is one thing a change does to a type's affinity matrix, worth a
// reviewer's eye when Warn is set.
type Note struct {
	Warn bool   `json:"warn"`
	Text string `json:"text"`
}

// Compare says what a change does to a type's affinity matrix, given the
// type before and after it, either nil where the type does not exist.
// Warnings come first: blocks the change joins, rows it makes span blocks,
// columns it makes written from two blocks.
func Compare(before, after *Report) []Note {
	switch {
	case after == nil && before == nil:
		return nil
	case after == nil:
		return []Note{{Text: "The type is removed."}}
	case before == nil:
		return created(after)
	}
	var c comparison
	c.joinedBlocks(before, after)
	c.newTies(before, after)
	c.newSharedWrites(before, after)
	c.blockCount(before, after)
	c.newHubs(before, after)
	c.newColumns(before, after)
	c.newRows(before, after)
	c.newEnvy(before, after)
	c.newUnused(before, after)
	return c.notes()
}

// comparison gathers Compare's notes, the warnings apart from the rest so
// that they come first.
type comparison struct{ warn, info []Note }

// add formats a note and files it with the warnings or the rest.
func (c *comparison) add(w bool, format string, args ...any) {
	n := Note{Warn: w, Text: fmt.Sprintf(format, args...)}
	if w {
		c.warn = append(c.warn, n)
	} else {
		c.info = append(c.info, n)
	}
}

// notes returns the warnings, then the rest.
func (c *comparison) notes() []Note {
	return append(c.warn, c.info...)
}

// joinedBlocks warns of blocks joined: columns that were in separate
// blocks now share one.
func (c *comparison) joinedBlocks(before, after *Report) {
	was := blockByColumn(before) // column → its block before
	for _, b := range after.Blocks {
		if b.Label == "core" {
			continue
		}
		parts, order := formerBlocks(b.Fields, was)
		if len(order) < 2 {
			continue
		}
		var sets []string
		for _, old := range order {
			sets = append(sets, fieldSet(parts[old]))
		}
		var through []string
		for _, m := range b.Methods {
			if spans(after, m, parts, order) {
				through = append(through, m)
			}
		}
		c.add(true, "Joins blocks that were apart: %s are now one block, %s%s.", strings.Join(sets, " and "), b.Label, via(through))
	}
}

// blockByColumn maps each column of r that is in a block other than the
// core to the block's label.
func blockByColumn(r *Report) map[string]string {
	was := map[string]string{}
	for _, b := range r.Blocks {
		if b.Label == "core" {
			continue
		}
		for _, f := range b.Fields {
			was[f] = b.Label
		}
	}
	return was
}

// formerBlocks groups the columns fs by the block was says each was in,
// leaving out those in none: the blocks in the order they first come up,
// and each one's columns.
func formerBlocks(fs []string, was map[string]string) (map[string][]string, []string) {
	parts := map[string][]string{} // block before → its columns among fs
	var order []string
	for _, f := range fs {
		old := was[f]
		if old == "" {
			continue
		}
		if _, ok := parts[old]; !ok {
			order = append(order, old)
		}
		parts[old] = append(parts[old], f)
	}
	return parts, order
}

// newTies warns of rows the change makes span blocks.
func (c *comparison) newTies(before, after *Report) {
	oldTies := names(before.Ties, func(t Tie) string { return t.Method })
	for _, t := range after.Ties {
		if !slices.Contains(oldTies, t.Method) {
			c.add(true, "%s now spans blocks %s: split it with Extract Method until each piece falls in one block, before splitting the type.",
				t.Method, describeBlocks(after, t.Blocks))
		}
	}
}

// newSharedWrites warns of columns the change makes written from two
// blocks.
func (c *comparison) newSharedWrites(before, after *Report) {
	oldShared := names(before.SharedWrites, func(s SharedWrite) string { return s.Field })
	for _, s := range after.SharedWrites {
		if !slices.Contains(oldShared, s.Field) {
			c.add(true, "%s is now written from blocks %s (%s): ownership follows writes, so let one block own it and the others pass the value in or ask the owner.",
				s.Field, strings.Join(s.Blocks, ", "), strings.Join(s.Writers, ", "))
		}
	}
}

// blockCount notes a change in the number of blocks.
func (c *comparison) blockCount(before, after *Report) {
	if b, a := realBlocks(before), realBlocks(after); a != b {
		c.add(false, "Blocks: %d, was %d. LCOM4 without hubs and bridges: %d, was %d.", a, b, after.LCOM4Split, before.LCOM4Split)
	}
}

// newHubs notes the columns that became hubs.
func (c *comparison) newHubs(before, after *Report) {
	for _, f := range after.Fields {
		if f.Hub && !slices.ContainsFunc(before.Fields, func(o Field) bool { return o.Name == f.Name && o.Hub }) {
			c.add(false, "%s is now used by most rows: a hub, set aside to see the blocks.", f.Name)
		}
	}
}

// newColumns notes the new columns and where they go.
func (c *comparison) newColumns(before, after *Report) {
	for _, f := range after.Fields {
		if slices.ContainsFunc(before.Fields, func(o Field) bool { return o.Name == f.Name }) {
			continue
		}
		switch {
		case f.Role == "unused":
			c.add(false, "New field %s is used by no method.", f.Name)
		case f.Block != "" && f.Block != "core":
			c.add(false, "New column %s (%s) goes to block %s.", f.Name, roleName(f.Role), f.Block)
		default:
			c.add(false, "New column %s (%s).", f.Name, roleName(f.Role))
		}
	}
}

// newRows notes the new rows, those in a block one by one, the others by
// pattern.
func (c *comparison) newRows(before, after *Report) {
	byKind := map[string][]string{}
	for _, m := range after.Methods {
		if slices.ContainsFunc(before.Methods, func(o Method) bool { return o.Name == m.Name }) {
			continue
		}
		if m.Kind == "" {
			c.add(false, "New method %s goes to block %s %s.", m.Name, m.Block, fieldSet(blockOf(after, m.Block).Fields))
		}
		byKind[m.Kind] = append(byKind[m.Kind], m.Name)
	}
	if ms := byKind["core"]; len(ms) > 0 {
		c.add(false, "New %s %s only the hubs.", methodsWord(ms), verb(ms, "uses", "use"))
	}
	if ms := byKind["bridge"]; len(ms) > 0 {
		c.add(false, "New bridge %s: %s only other methods, orchestration for a thin coordinator rather than any block.", methodsWord(ms), verb(ms, "it calls", "they call"))
	}
	if ms := byKind["free"]; len(ms) > 0 {
		c.add(false, "New %s %s no field: %s, the safest to move out.", methodsWord(ms), verb(ms, "uses", "use"), verb(ms, "a free function", "free functions"))
	}
}

// newEnvy notes the columns the change leaves read only through getters.
func (c *comparison) newEnvy(before, after *Report) {
	oldEnvy := names(before.Envy, func(e Envy) string { return e.Field })
	for _, e := range after.Envy {
		if !slices.Contains(oldEnvy, e.Field) {
			c.add(false, "%s is only read through %s: code outside the type works with it and may belong in its block.", e.Field, strings.Join(e.Getters, ", "))
		}
	}
}

// newUnused notes the new public methods that no caller outside the type
// uses.
func (c *comparison) newUnused(before, after *Report) {
	for _, m := range after.Unused {
		if !slices.Contains(before.Unused, m) && !slices.ContainsFunc(before.Methods, func(o Method) bool { return o.Name == m }) {
			c.add(false, "New public method %s has no callers outside the type: make it private or drop it.", m)
		}
	}
}

// created describes a type the change adds.
func created(r *Report) []Note {
	notes := []Note{{Text: fmt.Sprintf("New type: %s, %s, %s.",
		plural(len(r.Fields), "field"), plural(len(r.Methods), "method"), plural(realBlocks(r), "block"))}}
	for _, t := range r.Ties {
		notes = append(notes, Note{Warn: true, Text: fmt.Sprintf("%s spans blocks %s: split it before the type grows.", t.Method, describeBlocks(r, t.Blocks))})
	}
	for _, s := range r.SharedWrites {
		notes = append(notes, Note{Warn: true, Text: fmt.Sprintf("%s is written from blocks %s (%s).", s.Field, strings.Join(s.Blocks, ", "), strings.Join(s.Writers, ", "))})
	}
	return notes
}

// realBlocks counts the blocks other than the core.
func realBlocks(r *Report) int {
	n := 0
	for _, b := range r.Blocks {
		if b.Label != "core" {
			n++
		}
	}
	return n
}

// roleName names a column type as the technique does.
func roleName(role string) string {
	switch role {
	case "entity":
		return "entity state"
	case "policy":
		return "policy or config"
	}
	return role
}

// spans reports whether method m of r uses columns from at least two of
// the blocks that parts maps to their columns.
func spans(r *Report, m string, parts map[string][]string, order []string) bool {
	i := slices.IndexFunc(r.Methods, func(x Method) bool { return x.Name == m })
	if i < 0 {
		return false
	}
	n := 0
	for _, old := range order {
		if slices.ContainsFunc(r.Methods[i].Uses, func(u Use) bool { return slices.Contains(parts[old], r.Fields[u.Field].Name) }) {
			n++
		}
	}
	return n >= 2
}

func describeBlocks(r *Report, labels []string) string {
	var out []string
	for _, l := range labels {
		out = append(out, l+" "+fieldSet(blockOf(r, l).Fields))
	}
	return strings.Join(out, " and ")
}

func blockOf(r *Report, label string) Block {
	for _, b := range r.Blocks {
		if b.Label == label {
			return b
		}
	}
	return Block{Label: label}
}

// fieldSet writes fields as "{a, b, c}", eliding past four.
func fieldSet(fs []string) string {
	if len(fs) > 4 {
		return "{" + strings.Join(fs[:4], ", ") + fmt.Sprintf(", … %d more}", len(fs)-4)
	}
	return "{" + strings.Join(fs, ", ") + "}"
}

// methodsWord names methods: "method Save", "methods Save, Load".
func methodsWord(ms []string) string {
	if len(ms) == 1 {
		return "method " + ms[0]
	}
	return "methods " + strings.Join(ms, ", ")
}

// verb picks the singular or plural form for the subject ms.
func verb(ms []string, one, many string) string {
	if len(ms) == 1 {
		return one
	}
	return many
}

func via(ms []string) string {
	if len(ms) == 0 {
		return ""
	}
	return ", through " + strings.Join(ms, ", ")
}

func names[T any](xs []T, name func(T) string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = name(x)
	}
	return out
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
