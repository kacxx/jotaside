#!/usr/bin/env bash
# Tests for scripts/genericity-check.sh. Run: bash scripts/genericity-check.test.sh
#
# Every value that should be caught is joined from two pieces at run time, so
# this file holds none of them whole and passes the check itself.
set -u
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
check="$here/genericity-check.sh"
# The cases set their own deny patterns; CI's secret mustn't reach them.
unset GENERICITY_DENY_PATTERNS

pass=0; failures=0
ok() { pass=$((pass + 1)); printf '  ok   %s\n' "$1"; }
bad() { failures=$((failures + 1)); printf '  FAIL %s\n' "$1"; }

# run <label> <text>: checks the text as file <label>; sets out and rc.
# It runs from the repo root, whose go.mod and go.sum list the dependencies.
run() { out=$(cd "$here/.." && printf '%s\n' "$2" | GENERICITY_DENY_PATTERNS=${DENY:-} bash "$check" --stdin "$1" 2>&1); rc=$?; }
# clean <desc> <label> <text> / hit <desc> <label> <text> [rule]: hit's rule
# defaults to $rule, set per section.
clean() {
  run "$2" "$3"
  if [ "$rc" -eq 0 ]; then ok "$1"; else bad "$1: exit $rc: $out"; fi
}
hit() {
  local r=${4:-$rule}
  run "$2" "$3"
  case $rc:$out in
    "1:"*"FAIL [$r] $2:1"*) ok "$1" ;;
    *) bad "$1: expected a [$r] hit at $2:1, got exit $rc: $out" ;;
  esac
}

home=/Us; name=ers/jsmith-real
win='C:\Us'; wname='ers\jsmith-real'
addr=jane.doe; dom=@somecorp.io
gh=github.com; priv=/somecorp-internal/billing-service

echo "machine paths"; rule=path
hit "a home folder with a real-looking name" README.md "see ${home}${name}/code/app"
hit "a Linux home folder" README.md "in /ho""me/jsmith-real/.config"
hit "a Windows home folder" docs/setup.md "at ${win}${wname}\\go\\bin"
hit "a Windows home folder, escaped in JSON" x.json '"C:\\Us''ers\\jsmith-real\\go"'
clean "a placeholder home folder" README.md "/Users/you/go/bin/jotaside and /home/user/x"
clean "the CI runner's home" ci.log "/home/runner/work/repo"
clean "a placeholder Windows home" docs/setup.md 'C:\Users\you\go\bin\jotaside.exe'
clean "a placeholder with a space" x_test.go '"/Users/John Smith/go/bin/jotaside"'
hit "a home folder as Claude Code names a project folder" x.json "\"/p/-Us""ers-jsmith-real-code-app/1.jsonl\""
clean "  with a placeholder name" x.json '"/p/-Users-dev-src-webapp/1.jsonl"'
clean "  not a flag such as --home-dir" README.md 'run with --home-dir x'

echo "email addresses"; rule=email
hit "a real-looking address" README.md "mail ${addr}${dom} for access"
hit "an address at the start of a line" README.md "${addr}${dom}"
clean "an example.com address" x_test.go 'GIT_AUTHOR_EMAIL=t@example.com'
clean "a GitHub noreply address" commits 'Author: 1+someone@users.noreply.github.com'
clean "the co-author trailer" commits 'Co-Authored-By: Claude <noreply@anthropic.com>'
clean "Dependabot's sign-off" commits 'Signed-off-by: dependabot[bot] <support@github.com>'
clean "a git@ SSH remote" x_test.go 'git@github.com:o/n.git'
clean "credentials in a URL" x_test.go 'https://u:p@github.com/o/n and https://x:ghs_T/CD@github.com/o/n'
clean "a versioned tool, not an address" AGENTS.md 'go run example.com/tool@v2.14.0 and actions/checkout@v7'
clean "licence authors in the generated notices" THIRD_PARTY_NOTICES "Copyright (c) Someone <${addr}${dom}>"

echo "repo references"; rule=repo
hit "a private-looking owner/repo in prose" README.md "the code is in ${gh}${priv}"
hit "a private-looking owner/repo as a URL" README.md "https://${gh}${priv}/pull/3"
hit "a private-looking owner/repo#N in prose" README.md "see ${priv#/}#12"
hit "an SSH remote to a private-looking repo" x_test.go "git@${gh}:${priv#/}.git"
hit "an owner that only starts like an allowed one" README.md "https://${gh}/kacxx""corp/x"
clean "a Go module path in go.mod" go.mod "require ${gh}${priv} v1.2.3"
clean "a Go module path in go.sum" go.sum "${gh}${priv} v1.2.3 h1:abc="
clean "an import of a module go.sum lists" cmd/x/main.go '"github.com/mattn/go-isatty"'
clean "a tool's module path in AGENTS.md" AGENTS.md "go run ${gh}${priv}/cmd/tool@v1.0.0 run"
hit "  but not as a URL there" AGENTS.md "https://${gh}${priv}"
hit "  nor in another file" README.md "go run ${gh}${priv}/cmd/tool@v1.0.0 run"
clean "this project's repos" README.md 'https://github.com/kacxx/jotaside and kacxx/jotaside#54'
clean "placeholder repos" x_test.go 'https://github.com/o/n/issues/7, Org/ABC-1#2, ssh://git@github.com:22/kacxx/jotaside'
clean "a GitHub Action" ci.yml 'uses: actions/checkout@v7'
clean "an issue number alone" commits 'Part of #54'
clean "a link to a numbered heading" README.md '[why](docs/decisions.md#12-why)'
clean "GitHub's own pages" README.md 'https://github.com/settings/tokens and https://github.com/marketplace/actions/x'
clean "an allowed owner under orgs/ and the API's repos/" README.md 'https://github.com/orgs/kacxx/projects/1 https://api.github.com/repos/kacxx/jotaside'
hit "a private-looking owner under orgs/" README.md "https://${gh}/orgs${priv}"

echo "deny patterns"
DENY="some""corp" hit "a line matching a deny pattern" README.md "made at SomeCorp" deny
DENY="some""corp" clean "a line matching none" README.md "made at home"
DENY=$'nothing\n\nsome''corp' hit "a pattern after a blank line" README.md "made at somecorp" deny
DENY='[unclosed' run README.md "harmless"
if [ "$rc" -eq 2 ]; then ok "an invalid pattern fails closed (exit 2)"; else bad "an invalid pattern: exit $rc: $out"; fi
case $out in *unclosed*) bad "  and its text isn't printed: $out" ;; *) ok "  and its text isn't printed" ;; esac

echo "output"
run README.md "mail ${addr}${dom}"
case $out in *"$addr"*|*somecorp*) bad "a hit doesn't print what it matched: $out" ;; *) ok "a hit doesn't print what it matched" ;; esac
out=$(bash "$check" --diff 2>&1); rc=$?
if [ "$rc" -eq 2 ]; then ok "--diff without a ref is bad usage"; else bad "--diff without a ref: exit $rc"; fi

echo "tree and diff modes"
repo=$(mktemp -d)
trap 'rm -rf "$repo"' EXIT
git -C "$repo" init -q
git -C "$repo" config commit.gpgsign false
git -C "$repo" config user.email t@example.com
git -C "$repo" config user.name t
printf 'one\ntwo\n' > "$repo/notes.md"
git -C "$repo" add notes.md && git -C "$repo" commit -qm one
out=$(cd "$repo" && bash "$check" 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then ok "a clean tree passes"; else bad "a clean tree: exit $rc: $out"; fi
printf 'one\ntwo\nmail %s\nfour\n' "${addr}${dom}" > "$repo/notes.md"
git -C "$repo" commit -qam two
out=$(cd "$repo" && bash "$check" --diff HEAD~1 2>&1); rc=$?
case $rc:$out in
  "1:FAIL [email] notes.md:3"*) ok "--diff reports an added line at its line number in the file" ;;
  *) bad "--diff: expected an [email] hit at notes.md:3, got exit $rc: $out" ;;
esac
out=$(cd "$repo" && bash "$check" 2>&1); rc=$?
case $rc:$out in
  "1:FAIL [email] notes.md:3"*) ok "the tree check finds it too" ;;
  *) bad "tree: expected an [email] hit at notes.md:3, got exit $rc: $out" ;;
esac
printf 'one\ntwo\nfour\n' > "$repo/notes.md"
git -C "$repo" commit -qam three
out=$(cd "$repo" && bash "$check" --diff HEAD~1 2>&1); rc=$?
if [ "$rc" -eq 0 ]; then ok "--diff ignores a removed line"; else bad "--diff on a removal: exit $rc: $out"; fi
mkdir -p "$repo/-Us""ers-jsmith-real-code"
: > "$repo/-Us""ers-jsmith-real-code/x.md"
git -C "$repo" add . && git -C "$repo" commit -qm four
out=$(cd "$repo" && bash "$check" 2>&1); rc=$?
case $rc:$out in
  "1:FAIL [path] file names:"*) ok "the tree check reads file names" ;;
  *) bad "file names: expected a [path] hit, got exit $rc: $out" ;;
esac
out=$(cd "$repo" && bash "$check" --diff HEAD~1 2>&1); rc=$?
case $rc:$out in
  "1:FAIL [path] file names:"*) ok "  and so does --diff, for an added file" ;;
  *) bad "--diff file names: expected a [path] hit, got exit $rc: $out" ;;
esac

printf '\npassed: %d  failed: %d\n' "$pass" "$failures"
[ "$failures" -eq 0 ]
