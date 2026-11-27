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
	var warn, info []Note
	add := func(w bool, format string, args ...any) {
		n := Note{Warn: w, Text: fmt.Sprintf(format, args...)}
		if w {
			warn = append(warn, n)
		} else {
			info = append(info, n)
		}
	}

	// Blocks joined: columns that were in separate blocks now share one.
	was := map[string]string{} // column → its block before
	for _, b := range before.Blocks {
		if b.Label == "core" {
			continue
		}
		for _, f := range b.Fields {
			was[f] = b.Label
		}
	}
	for _, b := range after.Blocks {
		if b.Label == "core" {
			continue
		}
		parts := map[string][]string{} // block before → its columns now in b
		var order []string
		for _, f := range b.Fields {
			if old := was[f]; old != "" {
				if _, ok := parts[old]; !ok {
					order = append(order, old)
				}
				parts[old] = append(parts[old], f)
			}
		}
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
		add(true, "Joins blocks that were apart: %s are now one block, %s%s.", strings.Join(sets, " and "), b.Label, via(through))
	}

	oldTies := names(before.Ties, func(t Tie) string { return t.Method })
	for _, t := range after.Ties {
		if !slices.Contains(oldTies, t.Method) {
			add(true, "%s now spans blocks %s: split it with Extract Method until each piece falls in one block, before splitting the type.",
				t.Method, describeBlocks(after, t.Blocks))
		}
	}
	oldShared := names(before.SharedWrites, func(s SharedWrite) string { return s.Field })
	for _, s := range after.SharedWrites {
		if !slices.Contains(oldShared, s.Field) {
			add(true, "%s is now written from blocks %s (%s): ownership follows writes, so let one block own it and the others pass the value in or ask the owner.",
				s.Field, strings.Join(s.Blocks, ", "), strings.Join(s.Writers, ", "))
		}
	}

	if b, a := realBlocks(before), realBlocks(after); a != b {
		add(false, "Blocks: %d, was %d. LCOM4 without hubs and bridges: %d, was %d.", a, b, after.LCOM4Split, before.LCOM4Split)
	}
	for _, f := range after.Fields {
		if f.Hub && !slices.ContainsFunc(before.Fields, func(o Field) bool { return o.Name == f.Name && o.Hub }) {
			add(false, "%s is now used by most rows: a hub, set aside to see the blocks.", f.Name)
		}
	}
	for _, f := range after.Fields {
		if slices.ContainsFunc(before.Fields, func(o Field) bool { return o.Name == f.Name }) {
			continue
		}
		switch {
		case f.Role == "unused":
			add(false, "New field %s is used by no method.", f.Name)
		case f.Block != "" && f.Block != "core":
			add(false, "New column %s (%s) goes to block %s.", f.Name, roleName(f.Role), f.Block)
		default:
			add(false, "New column %s (%s).", f.Name, roleName(f.Role))
		}
	}
	// New rows, those in a block one by one, the others by pattern.
	byKind := map[string][]string{}
	for _, m := range after.Methods {
		if slices.ContainsFunc(before.Methods, func(o Method) bool { return o.Name == m.Name }) {
			continue
		}
		if m.Kind == "" {
			add(false, "New method %s goes to block %s %s.", m.Name, m.Block, fieldSet(blockOf(after, m.Block).Fields))
		}
		byKind[m.Kind] = append(byKind[m.Kind], m.Name)
	}
	if ms := byKind["core"]; len(ms) > 0 {
		add(false, "New %s %s only the hubs.", methodsWord(ms), verb(ms, "uses", "use"))
	}
	if ms := byKind["bridge"]; len(ms) > 0 {
		add(false, "New bridge %s: %s only other methods, orchestration for a thin coordinator rather than any block.", methodsWord(ms), verb(ms, "it calls", "they call"))
	}
	if ms := byKind["free"]; len(ms) > 0 {
		add(false, "New %s %s no field: %s, the safest to move out.", methodsWord(ms), verb(ms, "uses", "use"), verb(ms, "a free function", "free functions"))
	}
	oldEnvy := names(before.Envy, func(e Envy) string { return e.Field })
	for _, e := range after.Envy {
		if !slices.Contains(oldEnvy, e.Field) {
			add(false, "%s is only read through %s: code outside the type works with it and may belong in its block.", e.Field, strings.Join(e.Getters, ", "))
		}
	}
	for _, m := range after.Unused {
		if !slices.Contains(before.Unused, m) && !slices.ContainsFunc(before.Methods, func(o Method) bool { return o.Name == m }) {
			add(false, "New public method %s has no callers outside the type: make it private or drop it.", m)
		}
	}
	return append(warn, info...)
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
