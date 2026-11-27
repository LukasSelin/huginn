#!/usr/bin/env bash
# PostToolUse hook: after Claude edits a Go file, check the complexity
# budgets and, when the edit breaks one, send Claude back to rework the
# change. Exit status 2 hands what is written to stderr back to Claude.
#
#   gocyclo, gocognit  per function, in .golangci.yml, run on the edited
#                      file's package
#   package size       functions and methods per package, in
#                      internal/limits, run on the whole module
#
# CI runs the same checks (golangci-lint and go test), so this only moves the
# failure from the pull request to the moment the code is written. Anything
# else that goes wrong here (no go, no golangci-lint, code that does not
# parse yet) is left to the compiler and CI.
set -u

input=$(cat)

# The edited file's path from the hook input. On Windows it arrives with
# escaped backslashes (C:\\src\\x.go); turn each run of them into a slash.
file=$(printf '%s' "$input" |
	sed -n 's/.*"file_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
	head -n 1 | tr -s '\\' '/')
case "$file" in
*_test.go) exit 0 ;; # the budgets leave tests out
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
command -v go >/dev/null 2>&1 || exit 0

report=""

size=$(go test ./internal/limits -run 'TestPackageSize$' -count=1 2>&1)
if printf '%s' "$size" | grep -q 'over its budget'; then
	report+=$(printf '%s\n' "$size" | sed -n 's/^.*limits_test\.go:[0-9]*: //p')
	report+=$'\n'
fi

if command -v golangci-lint >/dev/null 2>&1; then
	lint=$(golangci-lint run --enable-only gocyclo,gocognit --output.text.colors=false \
		--output.text.print-issued-lines=false --show-stats=false "$(dirname "$file")" 2>&1)
	if printf '%s' "$lint" | grep -q '(gocyclo)\|(gocognit)'; then
		report+=$(printf '%s\n' "$lint" | grep '(gocyclo)\|(gocognit)')
		report+=$'\n'
	fi
fi

[ -z "$report" ] && exit 0

cat >&2 <<MSG
This edit breaks the complexity budgets:

${report}
The code has outgrown its limits, so step back and redo the change rather
than working around them. Don't raise a budget (min-complexity in
.golangci.yml, maxFuncs or frozen in internal/limits), add //nolint, or fold
logic into fewer, longer functions to get under the package budget.

- A function over gocyclo or gocognit does several jobs: split out the steps,
  or turn a long switch that maps values into a table.
- A package over its function budget holds more than one job: move a
  coherent part of it into a package of its own (give it a layer in
  .golangci.yml), or remove functions it no longer needs.
- If the right split is a design decision, stop and propose it to the user
  before writing more code.
MSG
exit 2
