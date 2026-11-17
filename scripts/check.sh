#!/usr/bin/env bash
# Runs this repo's linters and unit tests: the linters first, then the tests.
# Exits non-zero if any failed. CI's lint job runs its --lint-only half; CI's
# test and notices jobs run the same tests as their own steps, and CI also
# runs the end-to-end test, the Windows tests and the genericity check.
#
#   scripts/check.sh              linters, then tests
#   scripts/check.sh --lint-only  linters only (CI's lint job)
#
# Linters: gofmt, golangci-lint v2.14.0, shellcheck (warnings and errors, on
# tracked *.sh, with .shellcheckrc) and the plugin manifest lint
# (ci/lint-skills.sh). Tests: go vet, go test -race, the THIRD_PARTY_NOTICES
# check and the self-tests of ci/lint-skills.sh and scripts/genericity-check.sh.
#
# Unless CI=true, a linter failure stops the run before the tests, so a lint
# slip shows in seconds; CI runs everything and reports it all. A missing
# linter (shellcheck, jq) is a warning that names it and where to get it, and
# fails the run only when CI=true, so a push from a machine without one isn't
# blocked. Go is required either way.
#
# golangci-lint: a golangci-lint on PATH is used when it's v2.14.0; otherwise
# it runs through `go run` at that version (the first run downloads it). Under
# CI=true a golangci-lint of another version fails the run: ci.yml installs it,
# so its version and this one have drifted apart.
#
# On Windows (Git Bash), gofmt is skipped, since a CRLF checkout makes it list
# every file (CI checks it on Linux), and go test runs without -race when cgo
# is off, since the race detector needs it.
set -u
cd "$(dirname "$0")/.." || exit 1

lint_only=0
case "${1:-}" in
  "") [ "$#" -eq 0 ] || { echo "usage: scripts/check.sh [--lint-only]" >&2; exit 2; } ;;
  --lint-only) lint_only=1 ;;
  *) echo "usage: scripts/check.sh [--lint-only]" >&2; exit 2 ;;
esac

golangci_version=2.14.0
failed=()

# run <name> <command...>: runs a check, recording it as failed if it fails.
run() {
  local name=$1; shift
  echo "== $name"
  "$@" || failed+=("$name")
}

# needs <tool> <where to get it>: whether the tool is on PATH. A missing one is
# a warning, or a failure under CI=true.
needs() {
  command -v "$1" > /dev/null 2>&1 && return 0
  if [ "${CI:-}" = true ]; then
    echo "FAIL $1 is not installed (required when CI=true; install it: $2)"
    failed+=("$1 (not installed)")
  else
    echo "WARN $1 is not installed, so its check was skipped (install it: $2)"
  fi
  return 1
}

if ! command -v go > /dev/null 2>&1; then
  echo "FAIL go is not installed (install it: https://go.dev/doc/install)"
  exit 1
fi

windows=0
case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) windows=1 ;; esac

gofmt_check() {
  local out
  if [ "$windows" -eq 1 ]; then
    echo "WARN gofmt skipped on Windows (CRLF checkouts); CI checks it on Linux"
    return 0
  fi
  out=$(gofmt -l .) || return 1
  [ -z "$out" ] && return 0
  echo "These files need gofmt -w:"
  printf '%s\n' "$out"
  return 1
}

golangci() {
  if command -v golangci-lint > /dev/null 2>&1 \
     && golangci-lint version 2>/dev/null | grep -q "version v\{0,1\}$golangci_version "; then
    golangci-lint run
  elif [ "${CI:-}" = true ] && command -v golangci-lint > /dev/null 2>&1; then
    echo "FAIL golangci-lint on PATH isn't v$golangci_version: ci.yml and scripts/check.sh pin different versions"
    return 1
  else
    go run "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$golangci_version" run
  fi
}

shell_lint() {
  local files=()
  while IFS= read -r -d '' f; do [ -f "$f" ] && files+=("$f"); done < <(git ls-files -z -- '*.sh')
  [ "${#files[@]}" -eq 0 ] || shellcheck --severity=warning "${files[@]}"
}

run gofmt gofmt_check
run golangci-lint golangci
if needs shellcheck https://github.com/koalaman/shellcheck#installing; then
  run shellcheck shell_lint
fi
# It checks for jq itself, warning or failing as needs does.
run lint-skills bash ci/lint-skills.sh

if [ "$lint_only" -eq 0 ]; then
  if [ "${#failed[@]}" -gt 0 ] && [ "${CI:-}" != true ]; then
    echo "== the tests were NOT run: fix the linters first"
  else
    run "go vet" go vet ./...
    if [ "$(go env CGO_ENABLED)" = 1 ]; then
      run "go test -race" go test -race ./...
    else
      echo "WARN cgo is off, so go test runs without -race"
      run "go test" go test ./...
    fi
    run notices bash scripts/third-party-notices.sh --check
    if needs jq https://jqlang.org/download/; then
      run "lint-skills tests" bash ci/__tests__/lint-skills.test.sh
    fi
    run "genericity-check tests" bash scripts/genericity-check.test.sh
  fi
fi

echo "=="
if [ "${#failed[@]}" -gt 0 ]; then
  printf 'check.sh FAILED: %s\n' "${failed[@]}"
  exit 1
fi
echo "check.sh passed"
