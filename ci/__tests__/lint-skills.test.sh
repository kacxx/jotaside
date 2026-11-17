#!/usr/bin/env bash
# Tests for ci/lint-skills.sh. Run: bash ci/__tests__/lint-skills.test.sh
#
# Each case builds a small plugin (two manifests and one skill) in a temp
# folder, copies the linter into its ci/ folder, changes one thing and runs the
# copy, which lints the folder it sits in. A stub claude comes first on PATH,
# so `claude plugin validate .` never runs the real CLI. Needs jq, as the
# linter does; the "without jq" cases hide it.
set -u
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
linter="$here/../lint-skills.sh"

command -v jq > /dev/null 2>&1 || { echo "lint-skills.test.sh needs jq, as ci/lint-skills.sh does" >&2; exit 1; }

pass=0; failures=0
ok() { pass=$((pass + 1)); printf '  ok   %s\n' "$1"; }
bad() { failures=$((failures + 1)); printf '  FAIL %s\n' "$1"; }
eq() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1: expected [$2], got [$3]"; fi; }
has() { case $3 in *"$2"*) ok "$1" ;; *) bad "$1: output lacked [$2]: $3" ;; esac; }

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# The stub claude logs its arguments and exits $CLAUDE_RC (0 unless set).
bin="$tmp/bin"
mkdir -p "$bin" "$tmp/empty"
# shellcheck disable=SC2016 # the stub's $* and ${CLAUDE_RC} expand when it runs
printf '#!/bin/sh\nprintf "%%s\\n" "$*" >> "%s/claude.log"\nexit "${CLAUDE_RC:-0}"\n' "$tmp" > "$bin/claude"
chmod +x "$bin/claude"

# plugin <name>: a fresh plugin at $tmp/<name> that passes; sets P. plugin.json
# sets version 1.0.0, marketplace.json sets none.
plugin() {
  P="$tmp/$1"
  rm -rf "$P"
  mkdir -p "$P/ci" "$P/.claude-plugin" "$P/skills/demo"
  cp "$linter" "$P/ci/"
  printf '{"name": "demo", "version": "1.0.0"}\n' > "$P/.claude-plugin/plugin.json"
  market
  skill 'name: demo' 'description: Does a thing.'
}
# market [version]: marketplace.json listing demo, with that version if given.
market() {
  printf '{"name": "demo", "owner": {"name": "x"}, "plugins": [{"name": "demo", "source": "./"%s}]}\n' \
    "${1:+, \"version\": \"$1\"}" > "$P/.claude-plugin/marketplace.json"
}
# skill <line>...: demo's SKILL.md with those frontmatter lines, then $BODY.
skill() {
  { echo ---; printf '%s\n' "$@"; echo ---; echo; echo '# Demo'; printf '%s\n' "${BODY:-}"; } > "$P/skills/demo/SKILL.md"
}
# lint: runs the copy in $P; sets out and rc.
lint() {
  : > "$tmp/claude.log"
  out=$(PATH="$bin:$PATH" bash "$P/ci/lint-skills.sh" 2>&1)
  rc=$?
}
passes() { if [ "$rc" -eq 0 ]; then ok "$1"; else bad "$1: exit $rc: $out"; fi; }
fails() { if [ "$rc" -eq 1 ]; then has "$1" "$2" "$out"; else bad "$1: expected exit 1, got $rc: $out"; fi; }

echo "a minimal plugin"
plugin base
lint; passes "passes"
has "  and counts the skill" "lint-skills: ok (1 skills)" "$out"
eq "  and runs claude plugin validate ." "plugin validate ." "$(cat "$tmp/claude.log")"
CLAUDE_RC=1 lint; fails "a claude plugin validate failure fails it" "FAIL claude plugin validate ."
bash "$P/ci/lint-skills.sh" extra > /dev/null 2>&1
eq "an argument is bad usage (exit 2)" 2 $?

echo "without jq"
# PATH is an empty folder, so no jq is found; the linter looks for jq before
# it runs anything else.
warned="WARN jq is not installed"
out=$(env -u CI PATH="$tmp/empty" "$BASH" "$P/ci/lint-skills.sh" 2>&1)
eq "CI unset: exits 0" 0 $?
has "  and warns, naming jq" "$warned" "$out"
out=$(PATH="$tmp/empty" CI=false "$BASH" "$P/ci/lint-skills.sh" 2>&1)
eq "CI=false: exits 0" 0 $?
out=$(PATH="$tmp/empty" CI=true "$BASH" "$P/ci/lint-skills.sh" 2>&1)
rc=$?
fails "CI=true: fails, naming jq" "FAIL jq is not installed"

echo "empty values fail"
plugin empty
skill 'name: demo' 'description:  # a comment'
lint; fails "an empty description followed by a comment" "description is empty"
skill 'name: demo' 'description: #todo'
lint; fails "a description that is only a comment" "description is empty"
skill 'name: demo' 'description:' '  # todo'
lint; fails "an empty description, then an indented comment line" "description is empty"
skill 'name: demo' 'description: ""'
lint; fails 'description: ""' "description is empty"
skill 'name: demo' "description: '  '"
lint; fails "a quoted description of spaces" "description is empty"
skill 'name: demo' 'description: >-'
lint; fails "a block description with no text" "description is empty"
skill 'name: demo' 'description: >-' 'other: x'
lint; fails "a block description followed by the next key" "description is empty"
skill 'name: # todo' 'description: Does a thing.'
lint; fails "a name that is only a comment" "frontmatter has no name"
skill 'description: Does a thing.'
lint; fails "no name key" "frontmatter has no name"
skill 'name: demo'
lint; fails "no description key" "frontmatter has no description"

echo "a # in quotes or in a block is text"
plugin text
skill 'name: demo' 'description: "Reads #7"'
lint; passes 'a quoted # passes: description: "Reads #7"'
skill 'name: demo' "description: '# x'"
lint; passes "a single-quoted value starting with #"
skill 'name: demo' 'description: >-' '  # Starts with a hash, and is text.'
lint; passes "a block description whose text starts with #"
skill 'name: demo # the directory' 'description: Does a thing. # a note'
lint; passes "name and description with trailing comments"
skill 'name: demo' 'description: Written in C#'
lint; passes "a # with no space before it is text"
skill 'name: "#demo"' 'description: Does a thing.'
lint; fails 'name: "#demo" keeps its #' "name is #demo, not its directory name demo"

echo "the rest of the frontmatter"
plugin shape
skill 'name: other' 'description: Does a thing.'
lint; fails "a name that isn't the directory's" "name is other, not its directory name demo"
printf 'name: demo\n' > "$P/skills/demo/SKILL.md"
lint; fails "no opening ---" "doesn't open with --- frontmatter"
printf -- '---\nname: demo\ndescription: x\n' > "$P/skills/demo/SKILL.md"
lint; fails "no closing ---" "frontmatter has no closing ---"
skill 'name: demo' 'description: Does a thing.' 'not a key'
lint; fails "a top-level line that isn't a key" "frontmatter line 3 isn't a key: not a key"

echo "scripts/ paths a SKILL.md names"
plugin scripts
# shellcheck disable=SC2016 # the literal ${CLAUDE_SKILL_DIR} is the point
BODY='Run ${CLAUDE_SKILL_DIR}/scripts/run.sh.' skill 'name: demo' 'description: Does a thing.'
lint; fails "a skill script that doesn't exist" "but skills/demo/scripts/run.sh doesn't exist"
mkdir -p "$P/skills/demo/scripts" && : > "$P/skills/demo/scripts/run.sh"
lint; passes "a skill script that exists, ending a sentence"
# shellcheck disable=SC2016 # literal backticks, as markdown
BODY='Then run `scripts/check.sh`.' skill 'name: demo' 'description: Does a thing.'
lint; fails "a bare scripts/ path found nowhere" "names scripts/check.sh, but neither"
mkdir -p "$P/scripts" && : > "$P/scripts/check.sh"
lint; passes "a bare scripts/ path at the repo root"
BODY='See docs/scripts/x.sh and the my-scripts/y.sh file.' skill 'name: demo' 'description: Does a thing.'
lint; passes "a scripts/ inside a longer path isn't checked"

echo "versions are compared only when both manifests set one"
plugin versions
lint; passes "plugin.json sets one, marketplace.json doesn't"
market 1.0.0
lint; passes "both set the same one"
market 1.1.0
lint; fails "both set different ones" "version differs: .claude-plugin/plugin.json 1.0.0, .claude-plugin/marketplace.json 1.1.0"
printf '{"name": "demo"}\n' > "$P/.claude-plugin/plugin.json"
lint; passes "marketplace.json sets one, plugin.json doesn't"

echo "a version tag the listing pins matches plugin.json's version"
plugin refs
ref() {
  printf '{"name": "demo", "owner": {"name": "x"}, "plugins": [{"name": "demo", "source": {"source": "github", "repo": "o/n", "ref": "%s"}}]}\n' "$1" > "$P/.claude-plugin/marketplace.json"
}
ref v1.0.0
lint; passes "the same version"
ref v0.9.0
lint; fails "a different version" "version differs: .claude-plugin/plugin.json 1.0.0, .claude-plugin/marketplace.json source.ref v0.9.0"
ref main
lint; passes "a branch, not a version tag, isn't compared"

echo "manifests"
plugin manifests
printf '{"name": ' > "$P/.claude-plugin/plugin.json"
lint; fails "a plugin.json that isn't JSON" ".claude-plugin/plugin.json: not valid JSON"
plugin manifests
rm "$P/.claude-plugin/marketplace.json"
lint; fails "no marketplace.json" ".claude-plugin/marketplace.json: missing"
plugin manifests
printf '{"name": "demo", "owner": {"name": "x"}, "plugins": [{"name": "other", "source": "./"}]}\n' > "$P/.claude-plugin/marketplace.json"
lint; fails "a marketplace.json without the plugin" "lists no plugin named demo"
plugin manifests
rm -r "$P/skills/demo"
lint; fails "no skills" "no skills/*/SKILL.md"

printf '\npassed: %d  failed: %d\n' "$pass" "$failures"
[ "$failures" -eq 0 ]
