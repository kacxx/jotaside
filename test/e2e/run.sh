#!/usr/bin/env bash
# End-to-end check of the documented install/use flow against a freshly built
# binary: install -> paths -> empty inbox -> CLI round-trip -> per-agent hook
# capture/passthrough -> MCP initialize. Portable: runs on a CI runner as-is and
# inside the clean-room container (test/e2e/Dockerfile).
#
# The database is redirected to a throwaway location, so running this never
# touches your real inbox.
set -u

TMP="$(mktemp -d)"
export JOT_DB="$TMP/e2e.db"
trap 'rm -rf "$TMP"' EXIT

PASS=0; FAIL=0; OBSERVE=0
ok()   { echo "  PASS: $1"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL: $1"; FAIL=$((FAIL+1)); }
note() { echo "  OBSERVE: $1"; OBSERVE=$((OBSERVE+1)); }
hr()   { echo; echo "=== $1 ==="; }
captured() { echo "$1" | grep -q "Jotted"; }

# Install from this checkout when run inside the repo (so CI exercises the code
# under review); otherwise install the published @latest (clean-room default).
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." 2>/dev/null && pwd || true)"

hr "1. install (no cgo; pure-Go sqlite)"
if [ -n "$REPO_ROOT" ] && grep -q '^module github.com/kacxx/aside-jot$' "$REPO_ROOT/go.mod" 2>/dev/null; then
  echo "  installing from checkout: $REPO_ROOT"
  ( cd "$REPO_ROOT" && go install ./cmd/aside ) && ok "go install ./cmd/aside" || bad "go install from checkout"
else
  echo "  installing published @latest"
  go install github.com/kacxx/aside-jot/cmd/aside@latest && ok "go install @latest" || bad "go install @latest"
fi
BIN="$(go env GOPATH)/bin/aside"
[ -x "$BIN" ] && ok "binary present at \$(go env GOPATH)/bin/aside" || bad "binary missing at $BIN"

hr "2. paths / PATH check"
"$BIN" paths
if command -v aside >/dev/null 2>&1; then ON_PATH=yes; else ON_PATH=no; fi
if [ "${ASIDE_E2E_EXPECT_NOT_ON_PATH:-0}" = "1" ]; then
  # The clean-room sets GOPATH under HOME with ~/go/bin off PATH, so this asserts
  # the real new-user case the README warns about: use the absolute binary path.
  [ "$ON_PATH" = no ] && ok "'aside' is NOT on bare PATH (new-user case reproduced)" \
                       || bad "'aside' unexpectedly on PATH (fidelity lost)"
else
  note "'aside' on bare PATH: $ON_PATH (environment-dependent; use the absolute binary path in configs)"
fi

hr "2b. aside setup against a throwaway home (never your real config)"
SHOME="$TMP/home"; mkdir -p "$SHOME/.claude" "$SHOME/.codex"
setup() { HOME="$SHOME" USERPROFILE="$SHOME" CLAUDE_CONFIG_DIR= CODEX_HOME= "$BIN" setup "$@" 2>&1; }
CSET="$SHOME/.claude/settings.json"
# Other settings, another hook and an old-name aside hook, to be merged into.
printf '%s\n' '{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo done"}]}],"UserPromptSubmit":[{"hooks":[{"type":"command","command":"jot hook claude"}]}]},"zed":1}' >"$CSET"
BEFORE="$(cat "$CSET")"
OUT="$(setup claude --dry-run --mcp)"; echo "$OUT" | sed 's/^/  /'
[ "$(cat "$CSET")" = "$BEFORE" ] && ok "setup claude --dry-run writes nothing" || bad "dry run changed settings.json"
echo "$OUT" | grep -q "claude mcp add --scope user aside -- $BIN mcp" && ok "--mcp prints the command" || bad "--mcp output"
OUT="$(setup claude)"; echo "$OUT" | sed 's/^/  /'
grep -q "jot hook" "$CSET" && bad "old jot hook entry kept" || ok "old jot hook entry replaced"
[ "$(grep -c 'hook claude' "$CSET")" = 1 ] && ok "exactly one claude hook" || bad "claude hook duplicated or missing"
grep -q '"echo done"' "$CSET" && grep -q '"theme": "dark"' "$CSET" && ok "other settings and hooks kept" || bad "other settings lost"
[ -n "$(ls "$SHOME/.claude"/settings.json.bak-* 2>/dev/null)" ] && ok "backup written" || bad "no backup"
echo "$OUT" | grep -q "docs/cursor.md" && ok "claude output mentions Cursor" || bad "no Cursor note"
# The written command is what an agent runs: it must capture a jot.
CMD="$(sed -n 's/.*"command": "\(.*aside hook claude\)".*/\1/p' "$CSET")"
CAP="$(printf '{"prompt":">> via written hook","cwd":"%s"}' "$TMP" | JOT_DB="$TMP/setup-hook.db" sh -c "$CMD" 2>&1)"
captured "$CAP" && ok "the written claude hook captures a jot" || bad "written hook output: $CAP"
setup claude | grep -q "nothing changed" && ok "second setup claude is a no-op" || bad "second setup claude changed something"
OUT="$(setup codex)"; echo "$OUT" | grep -q "/hooks" && ok "setup codex says to trust the hook" || bad "no trust reminder"
grep -q "$BIN hook codex" "$SHOME/.codex/hooks.json" && ok "codex hook written" || bad "codex hook missing"
echo '{ not json' >"$SHOME/.codex/hooks.json"
setup codex >/dev/null && bad "setup codex accepted a broken file" || \
  { [ "$(cat "$SHOME/.codex/hooks.json")" = '{ not json' ] && ok "unparseable hooks.json left untouched" || bad "broken hooks.json was modified"; }
setup cursor | grep -q "isn't supported" && [ ! -e "$SHOME/.cursor" ] && ok "setup cursor writes nothing" || bad "setup cursor"

hr "3. first run: empty inbox"
OUT="$("$BIN" inbox 2>&1)"; echo "$OUT"
echo "$OUT" | grep -qE '^#' && bad "inbox not empty on a fresh DB" || ok "inbox empty on first run"

hr "4. CLI round-trip (add / inbox / search / show / done)"
"$BIN" add "e2e note" >/dev/null && ok "add" || bad "add"
"$BIN" inbox | grep -q "e2e note" && ok "inbox lists it" || bad "inbox missing it"
"$BIN" search "e2e" | grep -q "e2e note" && ok "search finds it" || bad "search"
ID="$("$BIN" inbox | grep -oE '#[0-9]+' | head -1 | tr -d '#')"
if [ -n "$ID" ]; then
  "$BIN" show "$ID" | grep -q "e2e note" && ok "show #$ID" || bad "show"
  "$BIN" done "$ID" >/dev/null && ok "done #$ID" || bad "done"
else
  bad "could not parse a jot id from inbox"
fi

hr "4b. promote --dry-run (only gh repo view, no change)"
"$BIN" add "e2e promote me" >/dev/null
PID="$("$BIN" inbox | grep "e2e promote me" | grep -oE '#[0-9]+' | head -1 | tr -d '#')"
# A stub gh that succeeds silently, so a regression can't create a real issue.
# It prints no visibility, so promote treats the repo as possibly public.
mkdir -p "$TMP/stubbin"; printf '#!/bin/sh\nexit 0\n' >"$TMP/stubbin/gh"; chmod +x "$TMP/stubbin/gh"
DRY="$(PATH="$TMP/stubbin:$PATH" "$BIN" promote "$PID" --repo o/n --dry-run 2>&1)"; echo "$DRY" | sed 's/^/  /'
echo "$DRY" | grep -q "^repo:  o/n" && echo "$DRY" | grep -q "^title: e2e promote me" \
  && ok "promote --dry-run prints repo and title" || bad "promote --dry-run output"
echo "$DRY" | grep -q "couldn't check whether o/n is public" \
  && ok "promote --dry-run warns when it can't check the repo's visibility" || bad "promote --dry-run visibility warning"
"$BIN" show "$PID" | grep -q "(inbox)" && ok "dry run leaves the jot in the inbox" || bad "dry run changed the jot"
"$BIN" promote 999999 --repo o/n --dry-run >/dev/null 2>&1 && bad "promote of an unknown id exited 0" \
  || ok "promote of an unknown id exits non-zero"
REFUSED="$(printf 'y\n' | PATH="$TMP/stubbin:$PATH" "$BIN" promote "$PID" --repo o/n 2>&1)" && bad "promote with piped input and no --yes exited 0" \
  || { echo "$REFUSED" | grep -q "not a terminal" && ok "promote refuses to ask without a terminal" \
  || bad "promote failed, but not with the terminal refusal: $REFUSED"; }
REFUSED="$(PATH="$TMP/stubbin:$PATH" "$BIN" promote "$PID" --repo o/n --yes </dev/null 2>&1)" && bad "promote --yes with an unknown visibility exited 0" \
  || { echo "$REFUSED" | grep -q "pass --public" && ok "promote --yes refuses a repo it can't check without --public" \
  || bad "promote --yes failed, but not with the visibility refusal: $REFUSED"; }

hr "5. hooks: >> captured, normal passes through (per-agent schemas printed)"
for agent in claude codex cursor; do
  CAP="$(printf '{"prompt":">> note via %s","cwd":"%s"}' "$agent" "$TMP" | "$BIN" hook "$agent" 2>&1)"
  echo "  $agent >>     : ${CAP:-<empty>}"
  captured "$CAP" && ok "hook $agent captures a >> prompt" || bad "hook $agent did NOT capture a >> prompt"
  # Pass-through must be exact: Claude Code and Codex add any hook stdout to
  # the model's context, so they must print nothing; Cursor wants {"continue":true}.
  THRU="$(printf '{"prompt":"just a normal question","cwd":"%s"}' "$TMP" | "$BIN" hook "$agent" 2>/dev/null)"
  echo "  $agent normal : ${THRU:-<empty>}"
  case "$agent" in
    cursor) WANT='{"continue":true}' ;;
    *)      WANT='' ;;
  esac
  [ "$THRU" = "$WANT" ] && ok "hook $agent passes a normal prompt through" \
                        || bad "hook $agent pass-through printed '${THRU}', want '${WANT:-<nothing>}'"
done

hr "5b. hooks fail safe: an unwritable database still blocks the jot"
BROKEN_DB="$TMP/not-a-dir/e2e.db"
: >"$TMP/not-a-dir"   # a file where the data directory should be
for agent in claude codex cursor; do
  OUT="$(printf '{"prompt":">> keep me safe","cwd":"%s"}' "$TMP" | JOT_DB="$BROKEN_DB" "$BIN" hook "$agent" 2>"$TMP/hook.err")"; CODE=$?
  ERR="$(cat "$TMP/hook.err")"
  echo "  $agent broken : ${OUT:-<empty>} (exit $CODE)"
  # Claude Code and Codex blocks also exit 2 with the reason on stderr.
  case "$agent" in
    claude|codex) WANT_CODE=2; WANT_ERR="$(echo "$ERR" | grep -c ">> keep me safe")" ;;
    *)      WANT_CODE=0; WANT_ERR=1 ;;
  esac
  if [ "$CODE" -eq "$WANT_CODE" ] && [ "$WANT_ERR" -ge 1 ] && echo "$OUT" | grep -q "NOT saved" && echo "$OUT" | grep -q ">> keep me safe"; then
    ok "hook $agent blocks and reports NOT saved with the original text, exit $WANT_CODE"
  else
    bad "hook $agent did not fail safe on an unwritable database"
  fi
done

hr "6. MCP server: JSON-RPC initialize"
INIT='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"e2e","version":"0"}}}'
# Portable bounded wait (no GNU `timeout`, which macOS lacks): feed one request,
# close stdin, and hard-stop the server after a few seconds if it lingers.
"$BIN" mcp >"$TMP/mcp.out" 2>&1 <<<"$INIT" &
MCP_PID=$!
for _ in 1 2 3 4 5; do kill -0 "$MCP_PID" 2>/dev/null || break; sleep 1; done
kill "$MCP_PID" 2>/dev/null; wait "$MCP_PID" 2>/dev/null
MCP_OUT="$(head -c 600 "$TMP/mcp.out" 2>/dev/null)"
echo "  ${MCP_OUT:-<no output>}"
echo "$MCP_OUT" | grep -q '"result"' && ok "MCP responded to initialize" || bad "no MCP initialize response"

hr "SUMMARY"
echo "PASS=$PASS  FAIL=$FAIL  OBSERVE=$OBSERVE"
[ "$FAIL" -eq 0 ]
