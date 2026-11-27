# Paths and boundaries in the review

Status: phase 1 built (see [Phases](#phases)); phases 2 and 3 designed, not
built. Prototype and numbers from huginn's own last 15 merge requests (129
edits of existing production functions). The structure phase 1 adds is
planned in `design/review-paths.ofn`.

## The problem

The review says which existing code an edit reaches and how big the edit
is (`+8 stmts · cyclomatic 8 → 11`), but not what it does to the paths
through that code. To learn that, the reviewer reads the body, and for a
caller, the caller's body too.

A reviewer asks three things of an edit, in this order:

1. **Did the paths change?** If not, what did?
2. **Which ways out changed,** and under what conditions?
3. **Who is on the other side?** What the callers pass and do with what
   comes back, and what the function reaches outside itself.

The review should answer each without the reviewer opening the body, and
send them to the lines that need reading.

## What the prototype found

The prototype reduces a function to its *flow*, the decisions, loops, ways
out and boundary crossings with straight-line code left out, and compares
the flows on either side of the edit.

| Edits of existing functions | 129 |
| --- | --- |
| Paths unchanged | 64 |
| Extracted into functions the change adds | 9 |
| Same paths, lines edited or re-nested | 19 |
| New or removed paths | 37 |

A flow runs 1 to 35 lines where the body runs 3 to 110. 25 of the 65 edits
whose paths change have flows longer than 12 lines.

Noise it showed, each answered below:

- A renamed receiver (`s` → `a`) changed every line that used it.
- An edited line (`0o755` → `0o750`, `:=` → `=`) showed as one line
  removed and one added.
- A switch on a tag and one without (`switch { case t.kind == x }` →
  `switch t.kind { case x }`) showed as a rewrite.
- Closures all read `func literal` and lined up with the wrong one.
- A `defer func() { … }()` printed its whole body on one line.
- In an extraction, a generic line (`if err != nil → return nil, err`)
  matched a function the change adds that has nothing to do with it.

## The flow of a function

`internal/ast` gives a function's flow as a list of steps, each with a
depth, a kind, the text shown, the key compared, and its line.

| Kind | Steps |
| --- | --- |
| decision | `if`, `else if`, `else`, `switch`, `case`, `default`, `select` and its cases |
| loop | `for`, `for range` |
| way out | `return`, `panic`, `os.Exit`, `log.Fatal*` |
| jump | `break`, `continue`, `goto`, shown only after the decision they end |
| effect | a call into an I/O package, a write to a receiver field, `go`, `defer`, a channel send, an error taken from a call, an error dropped with `_ =` |
| closure | a function literal, named by where it goes |

A decision whose body is a single way out or jump is one step:
`if err != nil → return "", err`.

Keys, not texts, are compared, so that what the author named or spelled
differently is no change:

- The receiver is written `ʳ` on both sides.
- A case of a switch on a tag is keyed as the condition it tests: `case a, b`
  under `switch t.kind` is `t.kind == a || t.kind == b`, as a switch without
  a tag writes it.
- A closure is keyed by where it goes: `func literal → inScope` when
  assigned, `func literal in slices.SortFunc` when passed.
- `defer` and `go` with a function literal read `defer func` and nest the
  literal's flow under it.
- White space is collapsed and long texts cut, as in the rest of the review.

## Comparing two flows

`internal/review` compares the flows of an edit's two sides:

1. **Align** the steps by key, longest common subsequence first. A step
   found on both sides at another depth is **re-nested**: it moved under a
   new guard or out of one.
2. **Pair** a removed step with an added step next to it when they share
   half their tokens: one **edited** step, shown with the changed words
   marked.
3. **Follow extractions.** A removed step found in the flow of a function
   the change adds *and that the new side calls* is **moved** to it; one
   that pairs with a step there as in 2 is **moved and edited**. Only the
   functions it calls are searched, so generic steps don't land in
   unrelated ones. What is neither is listed as not reappearing.
4. **Cut to hunks.** A flow over 12 steps shows the changed steps with one
   step around them and the decisions enclosing them as a breadcrumb
   (`for range r.Files › if f.OldPath != ""`); unchanged runs fold into
   `… 9 steps`.

## The verdict

Each edit gets one line ahead of its counts, the first of these that holds:

- **Paths unchanged**, then the calls it starts and stops making, from the
  symbols' references: `paths unchanged · now calls inScope`.
- **Extracted into** the functions it calls, and how many steps don't
  reappear: `extracted into (*logReader).files · 4 steps moved and edited`.
- **Same paths**, with how many steps are edited or re-nested:
  `same paths · 3 steps re-nested under if getenv != nil`.
- **New and removed paths**, and the ways out that changed:
  `2 new paths, 1 removed · 1 new way out`.

The verdict goes in *Look here first* and on each change card, in the
text, markdown and JSON reports and on the page.

## Ways out

For a function that returns, each way out with the decisions enclosing it,
outermost first: `return sha, nil ⇐ getenv != nil ∧ CI sets a diff base`.
The last one is *otherwise*; the early returns above it are implied, not
negated. Ways out are shown when one is new, gone, or reached under a
changed condition, and left out when none changed.

## Across the boundary

**Callers.** For each production caller, its call site in one line: what
it passes for the parameters that new or edited decisions test, and what
it does with an error the function returns:

| Caller does | Seen as |
| --- | --- |
| returns it | `return …, err` after the call, unwrapped |
| wraps it | `fmt.Errorf("…%w", err)`, `errors.Join` |
| handles it | `if err != nil` without returning it |
| answers with it | `http.Error`, a status written |
| logs it | a call into `log` or `slog` |
| drops it | assigned to `_` |
| ignores it | the call is a statement on its own |

Semdiff already locates each caller; the call site is read from the
caller's file at the head.

**Where the error ends.** A caller that returns or wraps the error hands
it on, so the review follows it to that caller's callers, with
`semdiff.Index.Uses` on the head's tree, until the error is handled,
answered, logged, dropped or ignored. Each chain is one line, ending in
what happens to the error:

```
review.Target → review.Build returns → web.(*repoAPI).reviewAPI answers 400
review.Target → web.(*repoAPI).reviewOptions drops it
```

A chain also ends at a function nothing in the repository calls (`main`,
or a handler only passed as a value, which `Index.ValueUses` finds), after
five hops, or where it would come back to a function already on it.
Callers matched by method name only are followed and marked `approx.`, as
in the impact list. Production callers come first and callers in tests do
not end a chain. At most six chains are shown; the rest are counted by how
they end (`and 4 more: 3 answer, 1 logs`).

**Effects.** What the function reaches outside itself, from its flow:
I/O packages (`os`, `os/exec`, `io/fs`, `net`, `net/http`, `database/sql`,
`syscall`), goroutines and channels, writes to its receiver, and errors it
drops. Each marked new or gone.

## New code

New code stays folded away, since the diff shows it whole, but each new
function in the fold says how much it decides, `4 decisions · 3 ways out`,
and opening it shows its flow as an outline. Decisions are counted rather
than paths, since paths multiply with every decision in a row.

## Look here first

- An edit whose paths are unchanged leaves *Look here first*. It stays
  under *Changes* with its verdict, which names the calls it starts and
  stops making. What this lets through is an edit inside straight-line
  code (a value computed differently, an argument changed) that reaches
  far; the counts on its change card still say how far.
- An extraction whose steps all reappear weighs as a move, at the bottom.
- A new way out returning an error, whose chain ends dropped or ignored,
  is an entry of its own, after breaking API: it is the case where the
  diff looks safe and the call site is not.

## Where the code goes

- **`internal/ast`**: `Flow`, read from a file's source for one
  declaration. `Symbol` does not change, so the history cache does not
  either and needs no new version.
- **`internal/review`**: comparing flows, verdicts, ways out, call sites
  and the new focus entry. It already imports `ast`, `semdiff` and `git`,
  so the layers in `.golangci.yml` stay as they are.
- **`cmd/huginn` and `internal/web`**: the verdict line, the flow hunks on
  the change card, ways out, and the callers' table.

## Limits

- Syntactic, like the rest of huginn: no types, so a parameter is followed
  by name into the decisions that test it, and not through assignments.
- No control-flow graph. Graphs of 5 to 35 decisions don't read better
  than an indented outline, and the outline diffs.
- Errors are followed by what a caller does with the value right after
  the call. An error stored in a field or sent on a channel ends the chain
  as handled, which it may not be.
- A flow is per function; a closure's ways out leave the closure, not the
  function, and are not listed as the function's.

## Decisions

1. Edits whose paths are unchanged leave *Look here first* and stay under
   *Changes*, rather than weighing half or ranking as today.
2. New code shows its flow as an outline in its fold, with a count of
   decisions and ways out.
3. Errors are followed through callers that return or wrap them, to where
   they end, rather than stopping at one hop.

## Phases

1. Flow, comparison, verdict and ways out, in the reports and on the page,
   with the outline of new code. **Built.**
2. Across the boundary: call sites, where each error ends, and effects.
3. *Look here first*: unchanged paths out, extractions down, the new entry.

### What phase 1 changed from this design

Building it against huginn's history moved these:

- **Where the source comes from.** `ast.File` keeps no source, so
  `git.FileDiff` gained `OldSource` and `NewSource`, which only `DiffRange`
  fills; `ast.Flows` reads a file's functions from it. Neither `Symbol` nor
  the index cache changed.
- **A way out marked changed** is one returning something else under the
  same decisions as well as one reached under other decisions, so that an
  edited error message is one changed way out, not one new and one gone.
- **Closures are counted apart** in the verdict (`2 new closures`), not as
  new paths.
- **An edited step is cut only when its first difference is past the 72nd
  character**, keeping its first words and the words just before the
  difference: `if err != nil → … the merge request goes into"))`.
- **`x.f++` counts as a write** to the receiver, as `x.f += 1` does; without
  it, renaming the receiver of a method that increments a field read as a
  new effect.
- **A new function that only runs off its end** says how many decisions it
  has and leaves the count of ways out away, rather than `0 ways out`; one
  with none says `no decisions`.
- **Steps that moved as they were are left out of the hunks**, since the
  verdict counts them (`all 24 steps reappear`).
- Case texts of a type switch read `x is T1 or T2` as their condition.
