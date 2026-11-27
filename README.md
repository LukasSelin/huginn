# huginn

huginn shows a Go repository's git history per symbol instead of per line.
It parses every commit's Go files with the Go AST, works out which functions,
methods, types, vars and consts were added, deleted, renamed, moved, or had
their signature or body changed, and follows each declaration through renames
and moves under one stable ID. You can then read the life of `(*Indexer).Visit`
as one timeline, browse it per directory in a web UI, or ask what one commit
did semantically and who calls what it touched.

Formatting-only edits (gofmt runs, comment changes) produce no symbol changes,
so they don't clutter a symbol's history.

## Install and build

huginn needs Go 1.27.1 or later and git 2.31 or later on `PATH`; it drives the
git CLI rather than reading the object store itself.

The module path is plain `huginn`, so install from a checkout:

```bash
git clone https://github.com/LukasSelin/huginn.git
cd huginn
go install ./cmd/huginn
```

To build a binary in place and run the tests instead:

```bash
go build -o huginn ./cmd/huginn
go test ./...
```

## Usage

```
huginn symbols [-C dir] [-branch rev] [-tests] [-no-cache] name
huginn serve [-C dir] [-branch rev] [-tests] [-no-cache] [-addr host:port]
huginn semdiff [-C dir] [-include-generated] [-callers n] rev
huginn review [-C dir] [-base rev] [-head rev] [-format text|markdown|json] [-callers n] [folder ...]
huginn silos [-C dir] [-branch rev] [-no-cache] [-n count] [-leaves name,...]
huginn affinity [-C dir] [-branch rev] [-tests] [-no-cache] [-months n] [-hub share] [-min-co n] [-n count] [-json] [type]
```

`-C` points at the repository (default: the current directory), `-branch`
picks the revision to walk (default `HEAD`), and `-tests` keeps symbols
declared in `_test.go` files, which are left out by default.

### `huginn symbols`

Prints the life of every symbol whose qualified name matches, newest change
first. Methods are named as Go writes them: `(*Repo).Walk`, `Point.String`.
Run against this repository:

```
$ huginn symbols '(*Repo).Walk'
indexed 210 live symbols in 249ms (0 commits cached, 4 walked)
(*Repo).Walk  internal/git/walk.go:110  alive  2 changes
  2026-10-14  37dff2f  Lukas Selin       body                    stmts 45 → 2, cyclomatic 13 → 1
  2026-10-14  4861e4f  Lukas Selin       added                   func (*Repo) Walk(ctx context.Context, opts Options, fn func(*Commit) error) (err error)
```

### `huginn serve`

Indexes the history in the background and serves the journey, layers, map,
health, review and affinity pages at `http://localhost:7878`, opening on
the journey; the others are at `/layers`, `/map`, `/health`, `/review` (see
[`huginn review`](#huginn-review)) and `/affinity` (see
[`huginn affinity`](#huginn-affinity)). While indexing, the pages show
progress. Once the history is indexed, the server encodes and gzips the
responses the pages open with (the journey and the layers at every
step, the health report and knowledge silos, the map's preset windows)
before it reports ready, then the per-directory views in the background, so
the pages draw from memory.

The journey opens on the architecture being built: playback stacks the
packages in 3D, as the layers page draws them. Switch its view to *Map* (or
press `M`, and `A` to come back) to watch the packages as bubbles on the
journey map instead.
It starts above, where the stack is the journey map, and comes down to the
side as layers appear, turning slowly while it plays; each package drops
onto its layer as it is born and flashes when it moves to another. Story
mode, chapters, key moments, the Edits and Knowledge colours, the author
filter and the video export all work in it; drag to turn it yourself, or
pick *Auto* to hand the camera back.

The layers page stacks the packages in 3D, frame by frame: a package that
refers to no other package of the repository sits on the bottom layer, and
every other one a layer above the highest package it refers to, so every
reference points down. Each package keeps its place on the journey map
within its layer, so the view from above is the journey map and the view
from the side is the architecture's layering. Packages that refer to each
other in a cycle, which Go allows only through tests or build variants,
share a layer and the references between them are drawn red. Drag to turn
the view, scroll to zoom, and point at or pick a package to light up what
it refers to and what refers to it.

The affinity page shows what `huginn affinity` prints for one type, picked
from the types that look like they do too much: the methods against the
fields, shaded where a method uses a field of its own block, with a toggle
back to the order they were declared in to see the blocks scatter; point at
a method to light up its fields and the methods it calls. Below come the
blocks, the findings, the suggested order, the change coupling as a grid
of the methods that changed together, the clients with the interfaces they
could declare, and the names. A followed type or method on the journey
links to it. The report is built when first asked for and kept.

```
$ huginn serve
huginn: serving http://127.0.0.1:7878
huginn: indexed in 65ms (4 commits cached, 0 walked)
```

The UI is backed by a small JSON API:

| Route | Returns |
| --- | --- |
| `GET /api/status` | indexing progress, then live/total symbol counts and the time span |
| `GET /api/symbol/{id}` | one symbol's declaration and its full list of events |
| `GET /api/health` | trends over the whole history (exported symbols, complexity, restructuring, survival, test lines), hotspot files and owners |
| `GET /api/silos` | knowledge silos: packages and symbols ranked by concentration × criticality, each with its reasons, everyone's share of the live code, and pairs coupled across knowledge boundaries |
| `GET /api/silos/leave?author=name` | what would be orphaned or rest on one person if the named authors (repeat `author` for several) left now, ranked as a hand-over list |
| `GET /api/layers?step=commit\|week\|month` | every frame's live packages with their layer, and the references between them, marking those that run within a cycle; packages carry the journey's map layout |
| `GET /api/affinity/types?months=n` | the struct types with methods, ranked by how much they seem to do, with edits counted over the last `n` months (12; 0 for all history) |
| `GET /api/affinity?type=id\|name=dir.Type&months=n` | one type's affinity report, as `huginn affinity -json` prints it: the matrix, blocks, findings, change coupling, clients, names, suggested order and disagreements |
| `GET /api/review?head=rev&base=rev&path=dir` | a merge request's review, as `huginn review` reports it: `head` defaults to `HEAD`, `base` to the remote's default branch, and `path`, repeated, limits it to folders; works while indexing |
| `GET /api/review/options?head=rev` | what the review page offers to pick from: the branch checked out, the default target, every branch, and the directories holding Go code at `head` |

### `huginn silos`

Ranks the live code by knowledge risk, as the health page's *Knowledge
silos* section does. Who knows what comes from the same knowledge model the
journey uses: each author's weight in a symbol grows with what they wrote
and rewrote of it, fades with a half-life of about six months, and counts as
orphaned once they have not committed for six months (as of the last
commit). Test code is left out.

- **Silos.** For every live symbol and package: the top knower still around
  and their share, the bus factor (the fewest people still around who hold
  more than half of what those people know), and the *concentration*, the
  share lost if the top knower left. It is multiplied by a *criticality*
  made of fan-in (distinct callers, three times for callers in other
  packages, half for method calls matched by name only), cyclomatic
  complexity and semantic edits, each on a log scale.
- **`-leaves name,...`** treats the named authors as gone now and lists what
  would be orphaned (the people left would know less than a third of it) or
  would newly rest on one person, ranked by the share lost × criticality:
  the order to hand things over in.
- **Coupled across knowledge boundaries.** Symbol and package pairs that
  refer to each other or change in the same commits (each commit weighted by
  1/(n−1) for the n units it changed, huge commits left out), whose
  knowers' weights have a cosine similarity of at most 25%.

```
$ huginn silos -n 2
analyzed 673 live symbols in 46ms (39 commits cached, 0 walked)
as of 2026-10-27 · 1 author

Package silos (2 of 11)
   394.0  internal/ast
          Lukas Selin holds 100% · bus factor 1 · 392 call sites from 45 callers in 8 other packages (8 approx.) · cyclomatic 315 · 110 edits
   267.4  internal/journey
          Lukas Selin holds 100% · bus factor 1 · 33 call sites from 8 callers in 2 other packages (2 approx.) · cyclomatic 360 · 122 edits
…
```

### `huginn affinity`

Looks for the seams inside a type that does too much, by asking what is
already grouped together in it from four sides. Where they agree, there is
a type to extract.

- **The affinity matrix.** Every method against every field, marked R or W
  for read and written; atomic, `sync.Map`, `bytes.Buffer` and
  `strings.Builder` fields count as written through their mutating methods
  (`Store`, `Add`, `WriteString`, …). Each field gets a class: *state*
  (written by methods), *configuration* (never written), *collaborator* (a
  pointer, func, channel or interface never written), *temporary* (used
  only within one writer's calls), *infrastructure* (locks, loggers) or
  *unused*. A configuration or collaborator field most methods use is a
  *hub* and is set aside, as it would join everything. The state written
  by the same methods forms the core and other blocks of state; the other
  fields form service blocks with the methods that use them, and a service
  block called only from another is merged into it. A method writing state
  stays with it, a method reading state while using a service goes with the
  service (it can take the state as parameters), a method that only calls
  others joins their block if they share one and is a *bridge* otherwise,
  and one touching no field and calling no method is *free*. Every
  placement says why, and the rows and columns are reordered block by
  block.
- **Findings.** Glue methods (writing state and using a service: split them
  first), state written from two blocks, methods without which a block
  falls in two, bridges, free methods and the parameter type they share,
  hubs, methods needing only a hub, temporaries, fields always used
  together, getters through which code elsewhere works on a field, and
  unused fields. LCOM4 counts the groups of methods linked by a field or a
  call before the hubs and bridges are set aside; the block count is the
  same after.
- **Change coupling.** Which methods changed in the same commits in the
  last `-months` (12; 0 for all history), each commit weighted by 1/(n−1)
  for the n methods it changed. The commit that added the type and those
  changing more than half its methods (at least five), such as a receiver
  rename, are left out. Pairs that changed together at least `-min-co`
  times (2) across blocks are explained: one reaches the other through its
  calls (the boundary may be wrong), both read the same field (a rule may
  be duplicated), or neither (a concept is missing). Each block also lists
  the symbols elsewhere that changed with it in small commits.
- **Clients.** The packages, and the files of the type's own package, that
  call its methods or use them as values, grouped into roles by the
  methods they share. A client using one block is clean; one spanning
  several is a use case for a facade. Each package gets the interface it
  could declare for what it uses, so it depends on those methods only.
  Methods nothing calls are listed, with the interface they may implement.
- **Names.** Words the members' names share, fields used by exactly the
  same methods, fields that look like modes, and whether the groups the
  fields were declared in fall within one block.

It ends with an order of extractions (free methods first, then the blocks
that own their writes, most changed first) and the places where the
signals disagree. Without a type, it ranks the struct types by how much
they seem to do. `-json` prints the same report as the web page reads. Run
against this repository:

```
$ huginn affinity Server
type internal/web.Server  internal/web/server.go:36
28 methods · 12 fields · LCOM4 1 → 4 blocks · 16 commits in 12 months

                   index
                   ┆  done
                   ┆  ┆  err
                   ┆  ┆  ┆  settled
                   ┆  ┆  ┆  ┆  │ repo
                   ┆  ┆  ┆  ┆  │ ┆  reports
                   ┆  ┆  ┆  ┆  │ ┆  ┆  build
                   ┆  ┆  ┆  ┆  │ ┆  ┆  ┆  │ name
                   ┆  ┆  ┆  ┆  │ ┆  ┆  ┆  │ ┆  │ ui
                   ┆  ┆  ┆  ┆  │ ┆  ┆  ┆  │ ┆  │ ┆  uiEncoded
                   ┆  ┆  ┆  ┆  │ ┆  ┆  ┆  │ ┆  │ ┆  ┆  ║ opts (hub)
                   ┆  ┆  ┆  ┆  │ ┆  ┆  ┆  │ ┆  │ ┆  ┆  ║ ┆  settle (infra)
A  Index           W  W  W  W  │ R  ·  ·  │ ·  │ ·  ·  ║ R  R   → historyKey, pageURLs, detailURLs, loadSaved, +2
A  Handler         R  ·  ·  ·  │ ·  ·  ·  │ ·  │ ·  ·  ║ ·  ·   → commit (value), status (value), file, indexRoutes
B  buildReport     ·  ·  ·  ·  │ R  ·  ·  │ ·  │ ·  ·  ║ R  ·
B  commit          R  ·  ·  ·  │ R  R  R  │ ·  │ ·  ·  ║ ·  ·
…
E  health          ·  ·  ·  ·  │ ·  ·  ·  │ ·  │ ·  ·  ║ R  ·
…
br journeyAPI      ·  ·  ·  ·  │ ·  ·  ·  │ ·  │ ·  ·  ║ ·  ·   → story, journey
…
fn silosLeave      ·  ·  ·  ·  │ ·  ·  ·  │ ·  │ ·  ·  ║ ·  ·
…
Suggested order
  1. Move out journeySymbols, symbol, silosAPI and silosLeave: they touch no field and call no method
     all take *index
  2. Extract B (report): repo, reports and build with buildReport, commit, story and historyKey
     owns its writes · 5 edits in 4 commits · changes together 16% · its client uses only it · named report · 3 crossings
…
```

### `huginn semdiff`

Prints the semantic diff of one commit: each changed symbol with its old and
new signature, statement count and cyclomatic complexity, the declarations
that call it (`-callers` caps how many are listed), and the blast radius over
the whole tree. Generated files are left out unless `-include-generated` is
given.

```
$ huginn semdiff -callers 2 665b0d3
commit 665b0d3f6a481bb27c4f9e2f36db827db96164d3
…
    Treat generated sides of a file as absent instead of skipping it

+63 −28 lines → 5 semantic changes across 5 symbols
added 2 · body edit 3

Files
  internal/git/walk.go       +34 −28     4 symbols: added, body edit
  internal/git/walk_test.go  +29 −0      1 symbol: added

Changes

  [body edit] type git.parsed
    internal/git/walk.go:205
    - type parsed struct{f *ast.File; err error}
    + type parsed struct{ok bool; f *ast.File; err error}
    definition changed
    5 call sites
      git.(*walker).side  internal/git/walk.go:214  ×2
      git.(*walker).analyze  internal/git/walk.go:228  ×2
      … and 1 more

  [added] method git.(*walker).side
    internal/git/walk.go:214
    + func (*walker) side(isGo bool, path, hash string) (parsed, error)
    6 stmts · cyclomatic 6
    2 call sites
      git.(*walker).analyze  internal/git/walk.go:228  ×2
  …

Blast radius
  7 call sites (approx.) in 4 declarations, 1 package
    huginn/internal/git
  git.(*walker).side  internal/git/walk.go:214  ×3
  git.(*walker).analyze  internal/git/walk.go:228  ×2
  git.(*Repo).Walk  internal/git/walk.go:109  ×1 (approx.)
  git.(*walker).parse  internal/git/walk.go:280  ×1
```

When the commit starts or stops imports between the repository's packages,
or references from one package's declarations to another's, a *Relations*
section lists them, as `huginn review` does.

### `huginn review`

Reviews a merge request: everything its head changes since the merge base
with the branch it goes into, as one semantic diff. Point it at a folder to
review only that part of the repository:

```bash
huginn review services/billing                     # from inside the repository
huginn review ~/src/backend/services/billing       # the folder says which repository
huginn review -C ~/src/backend -head origin/feature services/billing
```

Folders are relative to `-C` (default: the current directory), and the
repository is the first folder's when `-C` is not given. Several folders, or
single files, can be given; none means the whole repository. A folder need
not exist in the working tree, since the merge request may add or remove it.

`-head` is the merge request's branch (default `HEAD`). `-base` is the
branch it goes into. Without it, huginn takes the target from GitLab's merge
request pipelines (`CI_MERGE_REQUEST_DIFF_BASE_SHA`, else
`origin/$CI_MERGE_REQUEST_TARGET_BRANCH_NAME`) or GitHub's pull request
workflows (`origin/$GITHUB_BASE_REF`), and otherwise the remote's default
branch (`origin/HEAD`, else `origin/main`, `origin/master`, `main` or
`master`). Either way the head is compared from the merge base, as the merge
request's own diff is.

The report puts first what the change reaches, then what it is made of:

- **Impact** is the blast radius: the existing declarations anywhere in the
  repository that use what changed. With folders, it says how much of that
  lies outside them, and in which packages; such callers are marked
  `outside`.
- **Relations** are the imports between packages that the merge request
  starts (`+`) and stops (`−`), each with the references along it, from a
  changed declaration to a declaration of the other package, and then the
  references it starts and stops along imports that stay. With folders,
  only relations with one end inside them are kept, and those that cross
  the boundary say so: *out of the scope* for a new dependency of the
  folder, *into the scope* for new code elsewhere that depends on it. A
  reference that follows a symbol the change renames counts as neither
  started nor stopped. Test files are left out, since nothing imports them;
  so are packages from outside the repository. References come from
  function bodies, so a package used only in signatures or type
  definitions shows the files that import it instead.
- **Files**, **Changes** and **Noise** are as in `huginn semdiff`.

```
$ huginn review -base dbe0403 -head 371dfd2 -callers 1 internal/web
review 371dfd2 into dbe0403
from merge base dbe0403 · 1 commit
scope: internal/web

+208 −5 lines → 26 semantic changes across 26 symbols
added 18 · body edit 8

Impact
  22 call sites (approx.) in 15 declarations, 2 packages
  outside the scope: 1 call site in 1 declaration, 1 package
    huginn/cmd/huginn
  web.TestPrebuilt  internal/web/prebuilt_test.go:17  ×5 (approx., test)
  web.(*Server).prebuild  internal/web/prebuilt.go:106  ×2
  web.TestSavedResponses  internal/web/saved_test.go:14  ×2 (approx., test)
  … and 12 more

Relations
  + huginn/internal/web → huginn/internal/silos  (out of the scope)
      web.(*Server).Index → silos.Build  internal/web/server.go:90  ×1
      … and 3 more

Files
  internal/web/prebuilt.go       +1 −1       1 symbol: body edit
…
```

`-format markdown` writes the same report for a merge request comment,
with the changes and files folded away; `-format json` writes it for other
tools. In a GitLab merge request pipeline, a job like this one posts it on
the merge request. It needs an image with Go, git and `glab`, and a token
that may comment, here in `GITLAB_TOKEN`; `GIT_DEPTH: 0` fetches enough
history to reach the merge base.

```yaml
huginn-review:
  rules:
    - if: $CI_PIPELINE_SOURCE == "merge_request_event"
  variables:
    GIT_DEPTH: 0
  script:
    - git clone --depth 1 https://github.com/LukasSelin/huginn.git /tmp/huginn
    - (cd /tmp/huginn && go install ./cmd/huginn)
    - huginn review -format markdown services/billing > review.md
    - glab mr note "$CI_MERGE_REQUEST_IID" --message "$(cat review.md)"
```

`huginn serve` has the same review as a page, at `/review`. Pick the
merge request's branch, the branch it goes into and any folders (each
field suggests what the repository has), and the page reviews it without
waiting for the history to be indexed. What it reviews is kept in the
address, as `/review?head=feature&path=services/billing`, so a review can be
shared or bookmarked. Under the numbers, a diagram lays the packages out
in three columns: in the middle what changed (with folders, the packages in
them), on the left the packages that call into it, and on the right those
it uses. Its lines are the calls into what changed, weighted by call sites,
and the imports and references the merge request starts and stops; point
at a package to trace its lines, and pick it to narrow the callers,
relations and changes listed below to the ones that involve it.

## Architecture

```
git log / cat-file ──► internal/git ──► internal/ast ──► internal/lineage ──► internal/web
   (git CLI)           walk commits     ParseSource,      stable IDs           HTTP UI + JSON
                                        Diff              ▲   │
                                                          │   ▼
                                                   .git/huginn cache
                       internal/git ──► internal/ast ──► internal/semdiff ──► huginn semdiff, review
                       Diff, DiffRange,                  callers, blast radius,
                       Tree                              relations
```

- **internal/git** runs one `git log --raw --numstat -z` process that streams
  commit metadata and changed paths, and one long-lived `git cat-file --batch`
  that serves blob contents. Each commit is diffed against its first parent.
  Blobs parsed for one commit are reused for the next, so along a linear
  history most files are read and parsed once. `Repo.Diff` and `Repo.Tree`
  give a single commit's file pairs and a whole tree for `semdiff`;
  `Repo.DiffRange` gives the file pairs between any two commits, such as a
  merge request's merge base and head, for `review`.
- **internal/ast** turns a Go file into symbols with `ParseSource`: kind,
  qualified name, canonical signature, line span, statement count, cyclomatic
  complexity, a body hash that ignores formatting and comments, and the
  symbols each body refers to. `Diff` matches the symbols of two revisions by
  name first, then by body similarity, to find renames and moves.
- **internal/lineage** applies those per-commit changes oldest first and hands
  each declaration a stable ID when it first appears, carrying it across
  renames, moves and edits. `Build` walks first-parent history; with a
  `Cache`, it only walks commits it has not seen before (see below).
- **internal/web** serves the timeline UI (embedded static files) and its
  JSON API from an in-memory index built once at startup.
- **internal/semdiff** reports one commit, or a merge request's whole
  range: changed symbols, the callers of each across the tree at the head,
  the blast radius, and the relations between packages it starts and stops,
  optionally limited to a scope of folders. The tree before the change is
  the tree after it with the changed files' old sides put back, so only
  one tree is read. **internal/review** finds a merge request's target and
  merge base and builds that report over its range, for `huginn review` and
  the review page.
- **internal/knowledge** is the knowledge model: each author's decayed weight
  in each live symbol, and who counts as departed. The journey rolls it up
  per package frame by frame; **internal/silos** reads it at the last
  revision, resolves callers among the live symbols with semdiff's index
  (the module path inferred by **internal/modpath**, as the journey does),
  and ranks silos, departures and cross-knowledge coupling. Both resolve
  callers over the history's live tree (`History.LiveFiles`), and count
  changes made together with **internal/cochange**.
- **internal/affinity** analyzes one type at the last revision: its
  methods' field references, now told apart into reads and writes by
  internal/ast, clustered into blocks; its methods' co-changes from the
  lineage; and its callers, from semdiff's index over the live tree (with
  method values, `Index.ValueUses`).

The packages are stacked in layers, and depguard rules in `.golangci.yml`
keep them that way: foundation (`ast`, `git`, `modpath`), history
(`lineage`, `knowledge`, `codemap`), analyses (`journey`, `health`,
`semdiff`), reports (`story`, `silos`, `review`), then `web` and `cmd/huginn`. A
package may import the layers below its own; analyses don't import each
other, and neither do reports. Only test files may import
`internal/gittest`. CI runs `golangci-lint run`, which fails on an import
that breaks these rules; a new package needs a layer in `.golangci.yml`
before others can import it.

Side effects stay in place the same way: only `internal/git` runs programs
(`os/exec`), and only `web` and `cmd/huginn` use the network. Outside
`cmd/huginn` and tests, code doesn't exit, print to stdout or make a root
context; it takes a `context.Context` and an `io.Writer` from its caller.

### Index cache

`symbols` and `serve` store each walked commit's symbol changes in
`.git/huginn/revisions.gob` (in the main repository's git directory, shared by
all worktrees), keyed by commit hash. On the next start huginn lists the
commits to index with `git rev-list`, reads the ones it already has, and walks
only the rest, so a restart after a few new commits walks just those. A commit's
changes depend only on the commit, so the cache stays valid when branches move
or when `-branch` changes; entries are never evicted. Pass `-no-cache` to walk
everything afresh, or delete `.git/huginn` to reset it.

The file starts with a format version. A cache from another version, or one
that cannot be decoded, is discarded and rebuilt. The version must be bumped
whenever `internal/ast` or `internal/git` would produce different changes for
the same commit.

Cold versus warm start, measured on Windows 11 with `huginn symbols main`:

| Repository | First-parent commits | Cold (`-no-cache`) | Warm | Cache file |
| --- | --- | --- | --- | --- |
| huginn | 4 | 120–180 ms | 40–50 ms | 72 KB |
| a 150-commit Go service | 150 | 2.0–3.0 s | 70–90 ms | 2.0 MB |

A warm start is dominated by starting git (`rev-parse`, `rev-list`) and
decoding the cache; its cost grows with the number of changes, not with the
size of the files they touched.

## Known limitations

- **Go only.** Other files count toward a commit's line totals but have no
  symbols.
- **Method calls are resolved by receiver name only.** References are
  collected per file without type checking. A call on the enclosing method's
  receiver (`ix.walk()` in `(*Indexer).Walk`) resolves to that receiver type,
  but `x.Name()` on any other value matches every method called `Name` in the
  tree. `semdiff` marks such callers `(approx.)`; `silos` counts them at half
  weight in fan-in and says how many there are.
- **First-parent history.** Lineage follows one line of history: a merge
  counts as one commit, diffed against its first parent, so the changes of a
  merged branch are attributed to the merge commit, its author and its date.
  Commits on the merged branch do not appear in a symbol's timeline.
- **Affinity is syntactic too.** Writes are seen in assignments, `++`,
  `&x.f`, sends, `delete`, `clear` and `close`, and through methods only
  for atomic, `sync.Map`, `bytes.Buffer` and `strings.Builder` fields;
  `&x.f` counts as a write. Aliases (`p := s.items; p[0] = v`) and
  `*s = T{}` are not followed, uses of other values of the type are not
  attributed to it, and promoted members only when the type embeds one
  field or a mutex. Classes are heuristics: a field read through a pointer
  is a collaborator, a field written only by one method and the methods it
  calls a temporary. Callers outside the receiver are matched by method
  name, from files that import the type's package or name it in a
  signature; interfaces a method may implement are matched by name only.
  Declaration groups and lines are those of the type's last semantic
  change, since a blank line alone changes nothing.
