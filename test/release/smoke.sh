#!/usr/bin/env bash
# Smoke test of a published release, run as a new user would install it: no
# Go, no checkout, nothing on PATH. Downloads the archive for this machine from
# the GitHub release, checks it against checksums.txt, then runs setup, a hook
# capture, the MCP server and backup. Runs on macOS, Linux and Windows (Git
# Bash) runners; see .github/workflows/release-smoke.yml.
#
# Two modes:
# - Clean (when CI is set, or CLEAN=1): the runner is a new machine, so
#   nothing is redirected. Setup writes ~/.claude and ~/.codex, the database
#   goes in the OS data folder, and the test checks those default locations.
#   It fails if any of them already exist.
# - Local (otherwise): the agent configs and the database are redirected to a
#   temporary folder, so your real ones are never touched. A jotaside already
#   on your PATH is reported, not failed.
#
# The checks track the latest release: a check for a feature newer than TAG
# is skipped (see at_least), so older tags still pass.
#
# Needs: gh (authenticated), python. TAG is the release tag (e.g. v0.1.1),
# the latest release if unset; REPO defaults to kacxx/jotaside.
set -u

REPO="${REPO:-kacxx/jotaside}"
TAG="${TAG:-$(gh release view --repo "$REPO" --json tagName --jq .tagName)}"
[ -n "$TAG" ] || { echo "no release tag"; exit 1; }
VERSION="${TAG#v}"
if [ -n "${CLEAN:-}" ] || [ -n "${CI:-}" ]; then CLEAN=1; MODE=clean; else CLEAN=; MODE=local; fi

PASS=0; FAIL=0; OBSERVE=0
ok()   { echo "  PASS: $1"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
note() { echo "  OBSERVE: $1"; OBSERVE=$((OBSERVE+1)); }
hr()   { echo; echo "=== $1 ==="; }
summary() { hr "SUMMARY ($OS/$ARCH, $TAG, $MODE)"; echo "PASS=$PASS  FAIL=$FAIL  OBSERVE=$OBSERVE"; }
# Windows runners have python but not always python3.
PY="$(command -v python3 || command -v python)"
# json FILE EXPR evaluates a Python expression over the parsed file as `d`.
json() { "$PY" -c "import json,sys; d=json.load(open(sys.argv[1], encoding='utf-8')); print($2)" "$1"; }
# at_least X.Y.Z reports whether the release under test is X.Y.Z or newer.
at_least() { "$PY" -c "import sys; v=lambda s: [int(p) for p in s.split('-')[0].split('.')]; sys.exit(0 if v(sys.argv[1]) >= v(sys.argv[2]) else 1)" "$VERSION" "$1"; }
# mode PATH prints a file's permission bits, e.g. 600.
mode() { stat -f %Lp "$1" 2>/dev/null || stat -c %a "$1"; }

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

# The home folder jotaside sees (os.UserHomeDir) and the default data folder
# (app.DataDir), in this shell's path form.
if [ "$OS" = windows ]; then USERHOME="$(cygpath -u "$USERPROFILE")"; else USERHOME="$HOME"; fi
if [ -n "${XDG_DATA_HOME:-}" ] && [ "${XDG_DATA_HOME#/}" != "$XDG_DATA_HOME" ]; then DATADIR="$XDG_DATA_HOME/jot"
elif [ "$OS" = windows ]; then DATADIR="$(cygpath -u "$LOCALAPPDATA")/jot"
elif [ "$OS" = darwin ]; then DATADIR="$USERHOME/Library/Application Support/jot"
else DATADIR="$USERHOME/.local/share/jot"
fi

TMP="$(mktemp -d)"
# The binary goes in a folder under HOME, as ~/.local/bin would be: setup
# refuses one in the temp directory, since `go run` builds there.
BINDIR="$USERHOME/.jotaside-smoke-$$/bin"
trap 'rm -rf "$TMP" "${BINDIR%/bin}"' EXIT

if [ -n "$CLEAN" ]; then
  unset JOT_DB CLAUDE_CONFIG_DIR CODEX_HOME
  DB="$DATADIR/jot.db"
  CSET="$USERHOME/.claude/settings.json"
  XSET="$USERHOME/.codex/hooks.json"
else
  export JOT_DB="$TMP/data/jot.db"
  export CLAUDE_CONFIG_DIR="$TMP/claude" CODEX_HOME="$TMP/codex"
  mkdir -p "$CLAUDE_CONFIG_DIR" "$CODEX_HOME"
  DB="$JOT_DB"
  CSET="$CLAUDE_CONFIG_DIR/settings.json"
  XSET="$CODEX_HOME/hooks.json"
fi

hr "0. mode: $MODE"
echo "  database:        $DB"
echo "  claude settings: $CSET"
echo "  codex hooks:     $XSET"
if [ -n "$CLEAN" ]; then
  for p in "$DATADIR" "$CSET" "$XSET"; do
    [ -e "$p" ] && bad "not a clean machine: $p already exists" || ok "$p doesn't exist yet"
  done
fi

hr "1. download $ARCHIVE from $REPO $TAG"
mkdir -p "$TMP/dl" "$BINDIR"
if gh release download "$TAG" --repo "$REPO" --dir "$TMP/dl" --pattern "$ARCHIVE" --pattern checksums.txt; then
  ok "downloaded the archive and checksums.txt"
else
  bad "download failed"; summary; exit 1
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
# Every later step runs the binary; without it they would all fail separately.
[ -f "$BIN" ] && ok "archive holds $EXE" || { bad "no $EXE in the archive"; summary; exit 1; }
[ -f "$BINDIR/README.md" ] && [ -f "$BINDIR/LICENSE" ] && ok "archive holds README.md and LICENSE" || bad "README.md or LICENSE missing"
if ! command -v jotaside >/dev/null 2>&1; then
  ok "no jotaside on PATH, as for a new user"
elif [ -n "$CLEAN" ]; then
  bad "a jotaside is already on PATH: $(command -v jotaside)"
else
  note "your installed jotaside is on PATH ($(command -v jotaside)); this test runs $BIN by its full path"
fi

hr "4. version"
OUT="$("$BIN" version 2>&1)"; echo "  $OUT"
[ "$OUT" = "jotaside $TAG" ] && ok "version prints $TAG" || bad "version printed '$OUT'"

hr "5. setup claude, first install"
OUT="$("$BIN" setup claude 2>&1)"; CODE=$?; echo "$OUT" | sed 's/^/  /'
[ "$CODE" -eq 0 ] && echo "$OUT" | grep -q "✓ Added the jotaside hook" && ok "setup claude added the hook" || bad "setup claude (exit $CODE)"
[ -f "$CSET" ] && ok "wrote $CSET" || bad "no $CSET"
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
if at_least 0.1.1; then
  OUT="$(printf '%s' "$CAPTURE" | sh -c "$CMD" 2>/dev/null)"; CODE=$?; echo "  $OUT (exit $CODE)"
  [ "$CODE" -eq 2 ] && echo "$OUT" | grep -q "✓ Already jotted #1" \
    && ok "the same jot re-sent is blocked, not saved again" || bad "repeat"
else
  note "repeat check skipped: added in v0.1.1"
fi
OUT="$(printf '{"session_id":"smoke-1","hook_event_name":"UserPromptSubmit","cwd":"%s","prompt":"just a question"}' "$TMP" | sh -c "$CMD" 2>&1)"; CODE=$?
[ "$CODE" -eq 0 ] && [ -z "$OUT" ] && ok "an ordinary prompt passes through silently" || bad "pass-through printed '$OUT' (exit $CODE)"
OUT="$("$BIN" inbox 2>&1)"; echo "$OUT" | sed 's/^/  /'
[ "$(echo "$OUT" | grep -c 'smoke test jot')" = 1 ] && ok "inbox lists the jot once" || bad "inbox"

hr "7. the database"
[ -f "$DB" ] && ok "database created at $DB" || bad "no database at $DB"
if [ "$OS" = windows ]; then
  note "owner-only permissions aren't checked on Windows"
else
  [ "$(mode "$(dirname "$DB")")" = 700 ] && ok "data folder is owner-only (700)" || bad "data folder mode $(mode "$(dirname "$DB")")"
  [ "$(mode "$DB")" = 600 ] && ok "database is owner-only (600)" || bad "database mode $(mode "$DB")"
fi
WANTDB="$DB"; [ "$OS" = windows ] && WANTDB="$(cygpath -w "$DB")"
"$BIN" paths 2>&1 | grep -qF "$WANTDB" && ok "paths shows $WANTDB" || bad "paths doesn't show $WANTDB: $("$BIN" paths 2>&1)"

hr "8. setup codex"
OUT="$("$BIN" setup codex 2>&1)"; echo "$OUT" | sed 's/^/  /'
grep -q "hook codex" "$XSET" 2>/dev/null && ok "codex hook written to $XSET" || bad "codex hook missing from $XSET"
echo "$OUT" | grep -q "/hooks" && ok "setup codex says to trust the hook" || bad "no trust reminder"

hr "9. MCP server: initialize"
INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}'
"$BIN" mcp >"$TMP/mcp.out" 2>&1 <<<"$INIT" &
MCP_PID=$!
for _ in 1 2 3 4 5; do kill -0 "$MCP_PID" 2>/dev/null || break; sleep 1; done
kill "$MCP_PID" 2>/dev/null; wait "$MCP_PID" 2>/dev/null
grep -q "\"version\":\"$TAG\"" "$TMP/mcp.out" && ok "MCP server answers initialize as $TAG" || bad "MCP: $(head -c 300 "$TMP/mcp.out")"

hr "10. uninstall: back up, then remove"
"$BIN" backup "$TMP/backup.db" >/dev/null 2>&1 && [ -s "$TMP/backup.db" ] && ok "backup wrote a copy" || bad "backup"
rm -f "$BIN"
[ ! -e "$BIN" ] && [ -f "$DB" ] && ok "binary removed; jots stay until their folder is deleted" || bad "remove"

summary
[ "$FAIL" -eq 0 ]
