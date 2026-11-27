#!/usr/bin/env bash
# PostToolUse hook: after Claude edits a Go file, look at two kinds of size.
#
#   function complexity  gocyclo and gocognit in .golangci.yml, run on the
#                        edited file's package. CI fails on these, so an edit
#                        over them is sent back (exit 2 hands stderr to
#                        Claude) to be split before more code is written.
#   package size         a design direction, not a limit. When a package that
#                        is past the guideline grows beyond its size at HEAD,
#                        Claude is asked whether the new code is still the
#                        package's job. The edit stands either way.
#
# Anything else that goes wrong here (no go, no golangci-lint, code that does
# not parse yet) is left to the compiler and CI.
set -u

# Past this many functions and methods (test files aside), a package that
# grows gets the question. It is not a target: the standard library has
# packages of several hundred, and in Go the package, not the function, is
# what hides an implementation, so splitting one has a cost of its own.
guideline=80

input=$(cat)

# The edited file's path from the hook input. On Windows it arrives with
# escaped backslashes (C:\\src\\x.go); turn each run of them into a slash.
file=$(printf '%s' "$input" |
	sed -n 's/.*"file_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
	head -n 1 | tr -s '\\' '/')
case "$file" in
*_test.go) exit 0 ;; # neither size counts tests
*.go) ;;
*) exit 0 ;;
esac

root=${CLAUDE_PROJECT_DIR:-$(git rev-parse --show-toplevel 2>/dev/null)}
root=$(printf '%s' "$root" | tr -s '\\' '/')
lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }
case "$(lower "$file")" in
"$(lower "$root")"/*) ;;
*) exit 0 ;; # not in this module
esac
cd "$root" || exit 0

dir=$(dirname "$file")
if [ "$(lower "$dir")" = "$(lower "$root")" ]; then
	rel=.
	spec='*.go'
else
	rel=${dir:${#root}+1}
	spec="$rel/*.go"
fi

# funcs counts the top-level functions and methods in the Go files given,
# tests aside. gofmt starts every one at the beginning of a line.
funcs() {
	local n=0 f c
	for f in "$@"; do
		case "$f" in *_test.go) continue ;; esac
		[ -f "$f" ] || continue
		c=$(grep -c '^func ' "$f")
		n=$((n + c))
	done
	echo "$n"
}

# Ask about a package once per new high, so growing it by one function at a
# time does not repeat the question for edits that add nothing.
nudge=""
now=$(funcs "$dir"/*.go)
if [ "$now" -gt "$guideline" ]; then
	was=$(git grep -c '^func ' HEAD -- ":(glob)$spec" ":(glob,exclude)${spec%.go}_test.go" 2>/dev/null |
		awk -F: '{ n += $NF } END { print n + 0 }')
	state="$(git rev-parse --git-dir 2>/dev/null)/huginn-package-size"
	asked=0
	if [ -f "$state" ]; then
		asked=$(awk -v p="$rel" '$1 == p { n = $2 } END { print n + 0 }' "$state")
	fi
	if [ "$now" -gt "$was" ] && [ "$now" -gt "$asked" ]; then
		printf '%s %s\n' "$rel" "$now" >>"$state"
		nudge="Package size, a design direction rather than a limit: $rel now declares $now functions and methods, up from $was at HEAD, past the guideline of $guideline. Ask whether the new code is still this package's job. It belongs here if it works on the package's own types and helpers, or if moving it out would mean exporting what is private today just to share it. If it is a separate job that could stand behind a small API of its own, propose that package to the user instead of growing this one. The edit stands either way."
	fi
fi

lint=""
if command -v golangci-lint >/dev/null 2>&1; then
	lint=$(golangci-lint run --enable-only gocyclo,gocognit --output.text.colors=false \
		--output.text.print-issued-lines=false --show-stats=false "$dir" 2>&1 |
		grep '(gocyclo)\|(gocognit)')
fi

if [ -n "$lint" ]; then
	{
		cat <<MSG
This edit breaks the per-function complexity budget in .golangci.yml, which CI enforces:

$lint

Split the function into steps, or turn a long switch that maps values into a
table. Don't raise min-complexity or add //nolint. The new helpers can stay in
the same package; whether the package itself should split is a separate design
question.
MSG
		if [ -n "$nudge" ]; then
			printf '\n%s\n' "$nudge"
		fi
	} >&2
	exit 2
fi

if [ -n "$nudge" ]; then
	printf '{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"%s"}}\n' "$nudge"
fi
exit 0
