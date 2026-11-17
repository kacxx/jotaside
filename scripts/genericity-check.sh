#!/usr/bin/env bash
# Checks text for things a public repo shouldn't hold: machine-specific home
# paths, email addresses, references to GitHub repos outside an allowlist, and
# any private deny patterns. A hit prints its rule, file and line, never the
# text it matched, so CI logs don't repeat it.
#
#   scripts/genericity-check.sh                # every tracked text file
#   scripts/genericity-check.sh --diff REF     # lines added since REF (REF...HEAD)
#   scripts/genericity-check.sh --stdin LABEL  # stdin, checked as if it were file LABEL
#
# Rules:
#   path   /Users/<name>/, /home/<name>/ or C:\Users\<name>\, or the same path
#          as Claude Code names a project folder (-Users-<name>-...), where
#          <name> isn't a placeholder (you, me, dev, user, runner, ...). Tree
#          mode checks tracked file names too.
#   email  an email address, unless it's an example domain, a GitHub noreply
#          address, GitHub's own (Dependabot signs off as support@github.com),
#          the co-author trailer's noreply address, a git@ SSH remote
#          or URL credentials (user:token@host). Not checked in the generated
#          THIRD_PARTY_NOTICES, whose licences name their authors.
#   repo   github.com/OWNER/REPO (also github.com:OWNER/REPO, with or without
#          a port) and OWNER/REPO#N, unless OWNER is on the allowlist below,
#          is a GitHub page rather than an owner (settings, marketplace, ...;
#          for orgs/, users/ and the API's repos/, the next part is the owner
#          checked), or OWNER/REPO is a Go module
#          this repo depends on (go.mod, go.sum). go.mod, go.sum and the
#          generated THIRD_PARTY_NOTICES aren't checked for it; in ci.yml and
#          AGENTS.md a module path without a scheme (a tool such as
#          golangci-lint, run with `go run`) is allowed.
#   deny   each line of GENERICITY_DENY_PATTERNS, an extended regex matched
#          without regard to case. An invalid pattern fails the run.
#
# Exits 0 when clean, 1 on a hit, 2 on bad usage or an invalid deny pattern.
set -uo pipefail

usage() { echo "usage: scripts/genericity-check.sh [--diff REF | --stdin LABEL]" >&2; exit 2; }
mode=tree arg=
case "${1:-}" in
  "") [ "$#" -eq 0 ] || usage ;;
  --diff|--stdin) [ "$#" -eq 2 ] && [ -n "$2" ] || usage; mode=${1#--}; arg=$2 ;;
  *) usage ;;
esac

# Stdin is read before the cd below, in case it's a relative path's content.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
[ "$mode" = stdin ] && cat > "$tmp/stdin"
cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)" || exit 2

# Placeholder names in example paths.
safe_users='you|me|x|u|dev|user|tester|test|runner|example|alice|bob|john|shared'
# Owners that are this project, placeholders in docs and tests, or public
# tools the docs and workflows name.
safe_owners='kacxx|example|acme|xacme|owner|xowner|org|my-org|abc-1|o|a|actions|github|golang|golangci|googleapis|koalaman'
# github.com/<page>/... paths that are GitHub's own pages, not an owner.
github_pages='settings|marketplace|features|topics|notifications|apps|collections|search|explore|login|about|pricing|enterprise|site|security|contact|new|codespaces|issues|pulls'
# OWNER/REPO of every GitHub-hosted Go module the build depends on.
deps=$(cat go.mod go.sum 2>/dev/null | grep -oE 'github\.com/[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+' | cut -d/ -f2,3 | sort -u | tr '[:upper:]' '[:lower:]')

path_re='/(Users|home)/[A-Za-z][A-Za-z0-9._-]*|[A-Za-z]:\\{1,2}Users\\{1,2}[A-Za-z][A-Za-z0-9._-]*|(^|[^A-Za-z0-9-])-(Users|home)-[A-Za-z][A-Za-z0-9._]*'
email_re='(^|[^A-Za-z0-9._%+:/-])[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}'
safe_email_re='@([a-z0-9-]+\.)*example\.(com|org|net)$|@users\.noreply\.github\.com$|^(support|noreply)@github\.com$|^noreply@anthropic\.com$|^git@'
repo_url_re='(https?://|ssh://|[A-Za-z0-9._%+-]+@)?github\.com(:[0-9]+/|[:/])[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+(/[A-Za-z0-9][A-Za-z0-9-]*)?'
# A numbered anchor (decisions.md#12-why) isn't an issue reference.
repo_ref_re='(^|[^A-Za-z0-9_./-])[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+#[0-9]+([^A-Za-z0-9_-]|$)'

hits=0 scanned=0
hit() { printf 'FAIL [%s] %s:%s\n' "$1" "$2" "$3"; hits=$((hits + 1)); }

# The deny patterns, one per line; an invalid one fails closed, unprinted.
deny=()
while IFS= read -r p; do
  [ -n "$p" ] || continue
  printf '' | grep -iE -- "$p" > /dev/null 2>&1
  if [ $? -eq 2 ]; then
    echo "ERROR [deny] GENERICITY_DENY_PATTERNS has an invalid pattern (not printed)"
    exit 2
  fi
  deny+=("$p")
done <<< "${GENERICITY_DENY_PATTERNS:-}"

# allowed_repo <file> <match>: whether a repo reference is allowed in that file.
allowed_repo() {
  local file=$1 m=$2 or owner
  case $m in
    *github.com*)
      or=$(printf '%s' "$m" | sed -E 's#.*github\.com(:[0-9]+/|[:/])##')
      # A scheme-less module path in a file that runs Go tools.
      case $file in
        .github/workflows/ci.yml|AGENTS.md) case $m in *://*|*@*) ;; *) return 0 ;; esac ;;
      esac ;;
    *) or=$(printf '%s' "$m" | sed -E 's/^[^A-Za-z0-9]//; s/#[0-9]+.*$//') ;;
  esac
  or=$(printf '%s' "$or" | tr '[:upper:]' '[:lower:]')
  owner=${or%%/*}
  case $owner in
    orgs|users|repos|sponsors) or=${or#*/}; owner=${or%%/*} ;;
    *) printf '%s\n' "$owner" | grep -qxE "$github_pages" && return 0 ;;
  esac
  or=$(printf '%s' "$or" | cut -d/ -f1,2)
  or=${or%.git}
  printf '%s\n' "$owner" | grep -qxE "$safe_owners" && return 0
  printf '%s\n' "$deps" | grep -qxF -- "$or"
}

# scan <label> <text file> [<line map>]: checks the text file's lines. Line i
# is reported as the map's line i when there's a map, else as i.
scan() {
  local file=$1 text=$2 map=${3:-} i line m p
  scanned=$((scanned + 1))
  report() {
    local n=$2
    [ -n "$map" ] && n=$(sed -n "${2}p" "$map")
    hit "$1" "$file" "$n"
  }

  while IFS=: read -r i line; do
    while IFS= read -r m; do
      # The name: the part after Users or home and its separator.
      m=$(printf '%s' "$m" | sed -E 's/.*(Users|home)[-/\\]+//')
      if ! printf '%s\n' "$m" | grep -qixE "$safe_users"; then report path "$i"; break; fi
    done < <(printf '%s\n' "$line" | grep -oE "$path_re")
  done < <(grep -nE "$path_re" "$text")

  while IFS=: read -r i line; do
    while IFS= read -r m; do
      m=$(printf '%s' "$m" | sed -E 's/^[^A-Za-z0-9._%+-]//' | tr '[:upper:]' '[:lower:]')
      if ! printf '%s\n' "$m" | grep -qE "$safe_email_re"; then report email "$i"; break; fi
    done < <(printf '%s\n' "$line" | grep -oE "$email_re")
  done < <([ "$file" = THIRD_PARTY_NOTICES ] || grep -n '@' "$text")

  case $file in
    go.mod|go.sum|*/go.mod|*/go.sum|THIRD_PARTY_NOTICES) ;;
    *)
      while IFS=: read -r i line; do
        while IFS= read -r m; do
          if ! allowed_repo "$file" "$m"; then report repo "$i"; break; fi
        done < <(printf '%s\n' "$line" | grep -oE "$repo_url_re|$repo_ref_re")
      done < <(grep -nE "$repo_url_re|$repo_ref_re" "$text") ;;
  esac

  for p in ${deny[@]+"${deny[@]}"}; do
    while IFS=: read -r i _; do report deny "$i"; done < <(grep -niE -- "$p" "$text")
  done
}

case $mode in
  tree)
    while IFS= read -r -d '' f; do
      # Text files only: grep -I skips binaries (and empty files).
      [ -f "$f" ] && grep -qI '' "$f" && scan "$f" "$f"
    done < <(git ls-files -z)
    git ls-files > "$tmp/names"
    scan "file names" "$tmp/names"
    ;;
  diff)
    git rev-parse --verify -q "$arg^{commit}" > /dev/null || { echo "error: --diff: no such commit: $arg" >&2; exit 2; }
    while IFS= read -r -d '' f; do
      # The added lines, and each one's line number in the new file.
      git diff -U0 --no-color --no-ext-diff "$arg...HEAD" -- "$f" | awk -v text="$tmp/text" -v map="$tmp/map" '
        BEGIN { printf "" > text; printf "" > map }
        /^@@ / { split($3, a, ","); n = substr(a[1], 2) + 0; hunk = 1; next }
        !hunk { next }  # the diff header, including its +++ line
        /^\+/ { print substr($0, 2) > text; print n > map; n++ }'
      [ -s "$tmp/map" ] && scan "$f" "$tmp/text" "$tmp/map"
    done < <(git diff -z --name-only --diff-filter=d "$arg...HEAD")
    git diff --name-only --diff-filter=AR "$arg...HEAD" > "$tmp/names"
    [ -s "$tmp/names" ] && scan "file names" "$tmp/names"
    ;;
  stdin)
    scan "$arg" "$tmp/stdin"
    ;;
esac

if [ "$mode" = stdin ]; then printf 'Checked %s. ' "$arg"; else printf 'Checked %d file(s). ' "$scanned"; fi
if [ "$hits" -gt 0 ]; then
  echo "$hits genericity hit(s)."
  exit 1
fi
echo "No genericity hits."
