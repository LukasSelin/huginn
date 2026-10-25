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
huginn moves [-C dir] [-blocked] [packages]
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

Indexes the history in the background and serves the map, health and journey
pages at `http://localhost:7878`. While indexing, the pages show progress.
Once the history is indexed, the server encodes and gzips the responses the
pages open with (the journey at every step, the health report, the map's preset
windows) before it reports ready, then the per-directory views in the
background, so the pages draw from memory.

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

### `huginn moves`

Type-checks the working tree (`./...` unless packages are given) and lists the
functions that only one other package refers to, as candidates to move into
that package. Each comes with the unexported symbols of its package that only
it uses, which would move along (a type brings its methods), and the
`_test.go` files in its directory that name any of them. A move is blocked if
the function needs unexported symbols that code staying behind also uses, or
if it would break an import cycle or the internal package rule; those are
listed, with the reasons, only with `-blocked`. Methods are never suggested,
since they cannot leave their receiver's package.

References come from the type checker, plus the calls that interface dispatch
may make according to a class hierarchy analysis. Dispatched calls need
neither an import nor access, so they never block a move.

```
$ huginn moves
checked 12 packages in 996ms: 2 clean moves, 7 blocked
huginn/internal/journey.ParseStep  internal/journey/journey.go:34
  -> huginn/internal/web, used by (*Server).journeyAPI, (*Server).journeyPackage
…
```

## Architecture

```
git log / cat-file ──► internal/git ──► internal/ast ──► internal/lineage ──► internal/web
   (git CLI)           walk commits     ParseSource,      stable IDs           HTTP UI + JSON
                                        Diff              ▲   │
                                                          │   ▼
                                                   .git/huginn cache
                       internal/git ──► internal/ast ──► internal/semdiff ──► huginn semdiff
                       Diff, Tree                        callers, blast radius
```

- **internal/git** runs one `git log --raw --numstat -z` process that streams
  commit metadata and changed paths, and one long-lived `git cat-file --batch`
  that serves blob contents. Each commit is diffed against its first parent.
  Blobs parsed for one commit are reused for the next, so along a linear
  history most files are read and parsed once. `Repo.Diff` and `Repo.Tree`
  give a single commit's file pairs and a whole tree for `semdiff`.
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
- **internal/semdiff** reports one commit: changed symbols, the callers of
  each across the tree at that commit, and the commit's blast radius.
- **internal/moves** loads the working tree with `go/packages`, builds a
  reference graph between package-level symbols from `types.Info` and
  `go/callgraph/cha`, and finds functions to move with their helpers.

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
  tree. `semdiff` marks such callers `(approx.)`.
- **First-parent history.** Lineage follows one line of history: a merge
  counts as one commit, diffed against its first parent, so the changes of a
  merged branch are attributed to the merge commit, its author and its date.
  Commits on the merged branch do not appear in a symbol's timeline.
