#!/usr/bin/env bash
# Smoke test of a published release, run as a new user would install it: no
# Go, no checkout, nothing on PATH. Downloads the archive for this machine from
# the GitHub release, checks it against checksums.txt, then runs setup, a hook
# capture, the MCP server and backup against empty config folders and a
# throwaway database. Runs on macOS, Linux and Windows (Git Bash) runners; see
# .github/workflows/release-smoke.yml.
#
# Needs: gh (authenticated), python. TAG is the release tag (e.g. v0.1.1),
# the latest release if unset; REPO defaults to kacxx/jotaside.
set -u

REPO="${REPO:-kacxx/jotaside}"
TAG="${TAG:-$(gh release view --repo "$REPO" --json tagName --jq .tagName)}"
[ -n "$TAG" ] || { echo "no release tag"; exit 1; }
VERSION="${TAG#v}"

TMP="$(mktemp -d)"
# The binary goes in a folder under HOME, as ~/.local/bin would be: setup
# refuses one in the temp directory, since `go run` builds there.
BINDIR="$HOME/.jotaside-smoke-$$/bin"
trap 'rm -rf "$TMP" "${BINDIR%/bin}"' EXIT
export JOT_DB="$TMP/data/jot.db"
# Empty agent config folders, standing in for a new user's ~/.claude and ~/.codex.
export CLAUDE_CONFIG_DIR="$TMP/claude" CODEX_HOME="$TMP/codex"
mkdir -p "$CLAUDE_CONFIG_DIR" "$CODEX_HOME"

PASS=0; FAIL=0
ok()  { echo "  PASS: $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
hr()  { echo; echo "=== $1 ==="; }
# json FILE EXPR evaluates a Python expression over the parsed file as `d`.
# Windows runners have python but not always python3.
PY="$(command -v python3 || command -v python)"
json() { "$PY" -c "import json,sys; d=json.load(open(sys.argv[1], encoding='utf-8')); print($2)" "$1"; }

case "$(uname -s)" in
  Darwin) OS=darwin; EXT=tar.gz; EXE=jotaside ;;
  Linux) OS=linux; EXT=tar.gz; EXE=jotaside ;;
  MINGW*|MSYS*|CYGWIN*) OS=windows; EXT=zip; EXE=jotaside.exe ;;
  *) echo "unsupported OS $(uname -s)"; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported arch $(uname -m)"; exit 1 ;;
esac
ARCHIVE="jotaside_${VERSION}_${OS}_${ARCH}.${EXT}"

hr "1. download $ARCHIVE from $REPO $TAG"
mkdir -p "$TMP/dl" "$BINDIR"
if gh release download "$TAG" --repo "$REPO" --dir "$TMP/dl" --pattern "$ARCHIVE" --pattern checksums.txt; then
  ok "downloaded the archive and checksums.txt"
else
  bad "download failed"; echo "PASS=$PASS FAIL=$FAIL"; exit 1
fi

hr "2. checksum"
WANT="$(awk -v f="$ARCHIVE" '$2 == f { print $1 }' "$TMP/dl/checksums.txt")"
if command -v sha256sum >/dev/null 2>&1; then
  GOT="$(sha256sum "$TMP/dl/$ARCHIVE" | awk '{ print $1 }')"
else
  GOT="$(shasum -a 256 "$TMP/dl/$ARCHIVE" | awk '{ print $1 }')"
fi
[ -n "$WANT" ] && [ "$WANT" = "$GOT" ] && ok "sha256 matches checksums.txt" || bad "sha256 $GOT, checksums.txt has '${WANT}'"

hr "3. unpack to a folder under HOME that isn't on PATH"
if [ "$EXT" = zip ]; then
  powershell -NoProfile -Command "Expand-Archive -LiteralPath '$(cygpath -w "$TMP/dl/$ARCHIVE")' -DestinationPath '$(cygpath -w "$BINDIR")'"
else
  tar -xzf "$TMP/dl/$ARCHIVE" -C "$BINDIR"
fi
BIN="$BINDIR/$EXE"
[ -f "$BIN" ] && ok "archive holds $EXE" || bad "no $EXE in the archive"
[ -f "$BINDIR/README.md" ] && [ -f "$BINDIR/LICENSE" ] && ok "archive holds README.md and LICENSE" || bad "README.md or LICENSE missing"
command -v jotaside >/dev/null 2>&1 && bad "a jotaside is already on PATH: $(command -v jotaside)" || ok "no jotaside on PATH, as for a new user"

hr "4. version"
OUT="$("$BIN" version 2>&1)"; echo "  $OUT"
[ "$OUT" = "jotaside $TAG" ] && ok "version prints $TAG" || bad "version printed '$OUT'"

hr "5. setup claude, first install"
OUT="$("$BIN" setup claude 2>&1)"; CODE=$?; echo "$OUT" | sed 's/^/  /'
CSET="$CLAUDE_CONFIG_DIR/settings.json"
[ "$CODE" -eq 0 ] && echo "$OUT" | grep -q "✓ Added the jotaside hook" && ok "setup claude added the hook" || bad "setup claude (exit $CODE)"
echo "$OUT" | grep -q "none needed, the file did not exist" && ok "first install needs no backup" || bad "backup line for a new file"
CMD="$(json "$CSET" "d['hooks']['UserPromptSubmit'][0]['hooks'][0]['command']" 2>/dev/null)"
echo "  hook command: $CMD"
case "$CMD" in
  *"hook claude") ok "hook command runs 'hook claude'" ;;
  *) bad "hook command '$CMD'" ;;
esac
OUT="$("$BIN" setup claude 2>&1)"
echo "$OUT" | grep -q "nothing changed" && ok "second setup claude is a no-op" || bad "second setup claude: $OUT"
OUT="$("$BIN" setup claude --mcp --dry-run 2>&1)"
echo "$OUT" | grep -q "claude mcp add --scope user jotaside -- .* mcp" && ok "--mcp prints the claude mcp add command" || bad "--mcp output: $OUT"

hr "6. the written hook captures a jot"
CAPTURE="{\"session_id\":\"smoke-1\",\"hook_event_name\":\"UserPromptSubmit\",\"cwd\":\"$TMP\",\"prompt\":\">> smoke test jot\"}"
OUT="$(printf '%s' "$CAPTURE" | sh -c "$CMD" 2>/dev/null)"; CODE=$?; echo "  $OUT (exit $CODE)"
[ "$CODE" -eq 2 ] && echo "$OUT" | grep -q '"decision":"block"' && echo "$OUT" | grep -q "✓ Jotted #1" \
  && ok "a >> prompt is blocked and saved as #1" || bad "capture"
OUT="$(printf '%s' "$CAPTURE" | sh -c "$CMD" 2>/dev/null)"; CODE=$?; echo "  $OUT (exit $CODE)"
[ "$CODE" -eq 2 ] && echo "$OUT" | grep -q "✓ Already jotted #1" \
  && ok "the same jot re-sent is blocked, not saved again" || bad "repeat"
OUT="$(printf '{"session_id":"smoke-1","hook_event_name":"UserPromptSubmit","cwd":"%s","prompt":"just a question"}' "$TMP" | sh -c "$CMD" 2>&1)"; CODE=$?
[ "$CODE" -eq 0 ] && [ -z "$OUT" ] && ok "an ordinary prompt passes through silently" || bad "pass-through printed '$OUT' (exit $CODE)"
OUT="$("$BIN" inbox 2>&1)"; echo "$OUT" | sed 's/^/  /'
[ "$(echo "$OUT" | grep -c 'smoke test jot')" = 1 ] && ok "inbox lists the jot once" || bad "inbox"

hr "7. setup codex"
OUT="$("$BIN" setup codex 2>&1)"; echo "$OUT" | sed 's/^/  /'
grep -q "hook codex" "$CODEX_HOME/hooks.json" 2>/dev/null && ok "codex hook written" || bad "codex hook missing"
echo "$OUT" | grep -q "/hooks" && ok "setup codex says to trust the hook" || bad "no trust reminder"

hr "8. MCP server: initialize"
INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}'
"$BIN" mcp >"$TMP/mcp.out" 2>&1 <<<"$INIT" &
MCP_PID=$!
for _ in 1 2 3 4 5; do kill -0 "$MCP_PID" 2>/dev/null || break; sleep 1; done
kill "$MCP_PID" 2>/dev/null; wait "$MCP_PID" 2>/dev/null
grep -q "\"version\":\"$TAG\"" "$TMP/mcp.out" && ok "MCP server answers initialize as $TAG" || bad "MCP: $(head -c 300 "$TMP/mcp.out")"

hr "9. uninstall: back up, then remove"
"$BIN" backup "$TMP/backup.db" >/dev/null 2>&1 && [ -s "$TMP/backup.db" ] && ok "backup wrote a copy" || bad "backup"
"$BIN" paths 2>&1 | grep -q "jot.db" && ok "paths shows the database" || bad "paths"
rm -f "$BIN"
[ ! -e "$BIN" ] && [ -f "$JOT_DB" ] && ok "binary removed; jots stay until their folder is deleted" || bad "remove"

hr "SUMMARY ($OS/$ARCH, $TAG)"
echo "PASS=$PASS  FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
