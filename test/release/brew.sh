#!/usr/bin/env bash
# Smoke test of the Homebrew cask, run as a new macOS user would install it:
# `brew install kacxx/tap/jotaside`, then setup from PATH. Runs on a clean
# macOS runner; see .github/workflows/release-smoke.yml.
#
# The tap only serves the latest release, so a tag the tap doesn't hold is
# skipped, unless REQUIRE is true (the run right after a release), when the
# tap must already hold it.
#
# Needs: gh (authenticated), brew, python3. TAG is the release tag (e.g.
# v0.2.0), the latest release if unset; REPO defaults to kacxx/jotaside.
set -u

REPO="${REPO:-kacxx/jotaside}"
TAP="${TAP:-kacxx/tap}"
TAP_REPO="${TAP%%/*}/homebrew-${TAP#*/}"
TAG="${TAG:-$(gh release view --repo "$REPO" --json tagName --jq .tagName)}"
[ -n "$TAG" ] || { echo "no release tag"; exit 1; }
VERSION="${TAG#v}"

PASS=0; FAIL=0
ok()  { echo "  PASS: $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
hr()  { echo; echo "=== $1 ==="; }
summary() { hr "SUMMARY (brew, $TAG)"; echo "PASS=$PASS  FAIL=$FAIL"; }

hr "1. the tap holds $TAG"
HELD="$(gh api "repos/$TAP_REPO/contents/Casks/jotaside.rb" --jq .content 2>/dev/null | base64 --decode 2>/dev/null \
  | sed -n 's/^ *version "\(.*\)"$/\1/p')"
if [ "$HELD" = "$VERSION" ]; then
  ok "$TAP_REPO has jotaside $VERSION"
elif [ "${REQUIRE:-}" = true ]; then
  bad "$TAP_REPO has '${HELD:-no cask}', not $VERSION: did GoReleaser's cask push fail?"; summary; exit 1
else
  echo "  SKIP: $TAP_REPO has '${HELD:-no cask}', not $VERSION; the tap only serves the latest release"
  exit 0
fi

hr "2. brew install $TAP/jotaside"
command -v jotaside >/dev/null 2>&1 && bad "a jotaside is already on PATH: $(command -v jotaside)"
HOMEBREW_NO_AUTO_UPDATE=1 brew install "$TAP/jotaside" || { bad "brew install failed"; summary; exit 1; }
BIN="$(brew --prefix)/bin/jotaside"
[ "$(command -v jotaside)" = "$BIN" ] && ok "jotaside is on PATH at $BIN" || bad "jotaside on PATH is '$(command -v jotaside)'"

hr "3. runs without Gatekeeper's quarantine flag"
if xattr -p com.apple.quarantine "$BIN" >/dev/null 2>&1; then
  bad "the binary still has com.apple.quarantine"
else
  ok "no com.apple.quarantine on the binary"
fi
OUT="$(jotaside version 2>&1)"; echo "  $OUT"
[ "$OUT" = "jotaside $TAG" ] && ok "version prints $TAG" || bad "version printed '$OUT'"

hr "4. setup from PATH points the hook at the brew link"
# Kept out of ~/.claude: this checks the hook's path, not the default locations.
TMP="$(mktemp -d)"; trap 'rm -rf "$TMP"' EXIT
export CLAUDE_CONFIG_DIR="$TMP/claude" JOT_DB="$TMP/data/jot.db"
mkdir -p "$CLAUDE_CONFIG_DIR"
jotaside setup claude >/dev/null 2>&1 || bad "setup claude failed"
CMD="$(python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['hooks']['UserPromptSubmit'][0]['hooks'][0]['command'])" "$CLAUDE_CONFIG_DIR/settings.json" 2>/dev/null)"
echo "  hook command: $CMD"
case "$CMD" in
  *Caskroom*) bad "hook points into the Caskroom, so it breaks on brew upgrade" ;;
  *"$BIN"*"hook claude") ok "hook runs $BIN, which brew upgrade repoints" ;;
  *) bad "hook command '$CMD'" ;;
esac
OUT="$(printf '{"session_id":"brew-1","hook_event_name":"UserPromptSubmit","cwd":"%s","prompt":">> brew smoke jot"}' "$TMP" | sh -c "$CMD" 2>/dev/null)"
echo "$OUT" | grep -q "✓ Jotted #1" && ok "the hook captures a jot" || bad "capture printed '$OUT'"

hr "5. brew uninstall"
HOMEBREW_NO_AUTO_UPDATE=1 brew uninstall --cask jotaside >/dev/null 2>&1 && [ ! -e "$BIN" ] \
  && ok "uninstall removed $BIN" || bad "uninstall"

summary
[ "$FAIL" -eq 0 ]
