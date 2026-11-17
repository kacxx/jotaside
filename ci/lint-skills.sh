#!/usr/bin/env bash
# Lints the Claude Code plugin this repo ships: its two manifests and its
# skills. It needs no claude CLI, so it runs in CI.
#
#   ci/lint-skills.sh
#
# Checks:
#   - .claude-plugin/plugin.json and marketplace.json exist and parse, and
#     marketplace.json lists the plugin by plugin.json's name. The versions are
#     compared only when both files set one (marketplace.json here pins a git
#     ref instead, so plugin.json is usually the only place a version is set).
#   - every skills/*/SKILL.md opens with --- frontmatter whose `name` is its
#     directory's name and whose `description` isn't empty. Outside quotes a #
#     at the start or after a space begins a YAML comment, so
#     `description:  # todo` is empty; inside quotes it's text, so
#     `description: "Reads #7"` isn't.
#   - every scripts/... path a SKILL.md names exists: `${CLAUDE_SKILL_DIR}/scripts/x`
#     in that skill's folder, a bare `scripts/x` in the skill's folder or at the
#     repo root.
# With claude on PATH it also runs `claude plugin validate .`, without
# --strict (--strict also fails on warnings, such as the root CLAUDE.md not
# being loaded as plugin context, which is intended here).
#
# Needs bash, jq and POSIX tools. Exits 1 if a check failed, 2 on bad usage.
# Without jq it warns and exits 0, unless CI=true, where it fails: a missing
# linter shouldn't block a push from a laptop, but must never pass in CI.
set -u
shopt -s nullglob

[ "$#" -eq 0 ] || { echo "usage: ci/lint-skills.sh" >&2; exit 2; }
# Checked before anything else runs, so the test can hide jq with an empty PATH.
if ! command -v jq > /dev/null 2>&1; then
  if [ "${CI:-}" = true ]; then
    echo "FAIL jq is not installed (required when CI=true; https://jqlang.org/download/)"
    exit 1
  fi
  echo "WARN jq is not installed, so ci/lint-skills.sh checked nothing (install jq: https://jqlang.org/download/)"
  exit 0
fi
cd "$(dirname "$0")/.." || exit 1

errors=0
fail() { echo "FAIL $*"; errors=$((errors + 1)); }

# scalar <text>: an inline YAML value with its quotes, or a trailing comment,
# removed.
scalar() {
  local v=$1
  v=${v#"${v%%[![:space:]]*}"}
  case $v in
    \"*) v=${v#\"}; v=${v%%\"*} ;;
    \'*) v=${v#\'}; v=${v%%\'*} ;;
    \#*) v="" ;;
    *) v=$(printf '%s\n' "$v" | sed -e 's/[[:space:]]#.*//' -e 's/[[:space:]]*$//') ;;
  esac
  printf '%s' "$v"
}

# --- manifests
plugin=.claude-plugin/plugin.json
market=.claude-plugin/marketplace.json
parsed=1
for f in "$plugin" "$market"; do
  if [ ! -f "$f" ]; then
    fail "$f: missing"; parsed=0
  elif ! jq -e . "$f" > /dev/null 2>&1; then
    fail "$f: not valid JSON"; parsed=0
  fi
done
if [ "$parsed" -eq 1 ]; then
  name=$(jq -r '.name // empty' "$plugin" | tr -d '\r')
  version=$(jq -r '.version // empty' "$plugin" | tr -d '\r')
  if [ -z "$name" ]; then
    fail "$plugin: no name"
  else
    jq -e --arg n "$name" 'any(.plugins[]?; .name == $n)' "$market" > /dev/null \
      || fail "$market: lists no plugin named $name"
    while IFS= read -r v; do
      if [ -n "$version" ] && [ "$v" != "$version" ]; then
        fail "version differs: $plugin $version, $market $v"
      fi
    done < <(jq -r --arg n "$name" '.plugins[]? | select(.name == $n) | .version // empty' "$market" | tr -d '\r')
  fi
fi

# --- skills
skills=(skills/*/SKILL.md)
[ "${#skills[@]}" -gt 0 ] || fail "no skills/*/SKILL.md"
for md in ${skills[@]+"${skills[@]}"}; do
  dir=${md%/SKILL.md}
  want=${dir##*/}
  # The frontmatter is the lines between a first line of --- and the next ---.
  fm=$(tr -d '\r' < "$md" | awk '
    NR == 1 { if ($0 != "---") { bad = 3; exit } next }
    $0 == "---" { closed = 1; exit }
    { print }
    END { if (bad || NR == 0) exit 3; if (!closed) exit 4 }')
  case $? in
    0) ;;
    3) fail "$md: doesn't open with --- frontmatter"; continue ;;
    *) fail "$md: frontmatter has no closing ---"; continue ;;
  esac

  # A top-level line is a key; comments and continuations start with # or space.
  stray=$(printf '%s\n' "$fm" | grep -nEv '^([[:space:]]|#|$|[A-Za-z0-9_-]+:([[:space:]]|$))' | head -n 1)
  [ -z "$stray" ] || fail "$md: frontmatter line ${stray%%:*} isn't a key: ${stray#*:}"

  # name: read inline only, and must match the directory.
  n=$(scalar "$(printf '%s\n' "$fm" | sed -n 's/^name://p' | head -n 1)")
  if [ -z "$n" ]; then
    fail "$md: frontmatter has no name"
  elif [ "$n" != "$want" ]; then
    fail "$md: name is $n, not its directory name $want"
  fi

  # description: inline, or a block scalar (>, |, with - or +) followed by
  # indented lines. With no indicator, an indented line starting with # is a
  # comment; inside a block scalar it's text.
  if ! printf '%s\n' "$fm" | grep -q '^description:'; then
    fail "$md: frontmatter has no description"
  else
    raw=$(printf '%s\n' "$fm" | sed -n 's/^description:[[:space:]]*//p' | head -n 1)
    d=$(scalar "$raw")
    case $raw in
      \"*|\'*) [ -n "${d//[[:space:]]/}" ] || fail "$md: description is empty" ;;
      *)
        case $d in
          ''|'>'|'>-'|'>+'|'|'|'|-'|'|+')
            printf '%s\n' "$fm" | awk -v block="${d:+1}" '
              /^description:/ { on = 1; next }
              on && /^[^[:space:]]/ { exit }
              on && block != "1" && /^[[:space:]]*#/ { next }
              on && /[^[:space:]]/ { found = 1 }
              END { exit !found }' \
              || fail "$md: description is empty" ;;
        esac ;;
    esac
  fi

  # scripts/... paths it names. A bare match may carry the character before
  # it (a space, quote or backtick); only the part from scripts/ on is used.
  while IFS= read -r ref; do
    [ -n "$ref" ] || continue
    rel=scripts/${ref#*scripts/}
    rel=${rel%.}  # a full stop ending the sentence
    case $ref in
      *CLAUDE_SKILL_DIR*)
        [ -e "$dir/$rel" ] || fail "$md: names $ref, but $dir/$rel doesn't exist" ;;
      *)
        [ -e "$dir/$rel" ] || [ -e "$rel" ] \
          || fail "$md: names $rel, but neither $dir/$rel nor $rel exists" ;;
    esac
  done < <(grep -oE '(\$\{?CLAUDE_SKILL_DIR\}?/|(^|[^A-Za-z0-9_./-]))scripts/[A-Za-z0-9_./-]+' "$md" | sort -u)
done

# --- the CLI's own checks, when it's installed (CI doesn't have it)
if command -v claude > /dev/null 2>&1; then
  echo "== claude plugin validate ."
  claude plugin validate . || fail "claude plugin validate ."
fi

if [ "$errors" -gt 0 ]; then
  echo "lint-skills: $errors failed"
  exit 1
fi
echo "lint-skills: ok (${#skills[@]} skills)"
