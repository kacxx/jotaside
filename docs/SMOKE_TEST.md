# Smoke test

End-to-end check of the built binary against the hook fixtures. It uses a
throwaway database and never touches your real one.

```sh
go build -o bin/jotaside ./cmd/jotaside
export JOT_DB="$(mktemp -d)/jot.db"
```

`paths` reports the binary and warns when a bare `jotaside` would run something
else (or nothing), which is why the hook configs use absolute paths:

```sh
bin/jotaside paths
# db:           /tmp/tmp.XXXX/jot.db ($JOT_DB)
# busy timeout: 2s
# binary:       /…/bin/jotaside
# warning:      'jotaside' is not on PATH; use /…/bin/jotaside in hook and MCP configs
```

## Claude Code hook

Ordinary prompts, mid-prompt `>>` and malformed payloads must print **nothing**:

```sh
for f in normal mid_prompt malformed; do
  printf '%s: [' $f; bin/jotaside hook claude < testdata/hooks/claude/$f.json; echo "] exit=$?"
done
# normal: [] exit=0
# mid_prompt: [] exit=0
# malformed: [] exit=0
```

A jot is blocked and saved. Point `cwd` at a real repo to get git context:

```sh
jq --arg cwd "$PWD" '.cwd=$cwd' testdata/hooks/claude/capture.json | bin/jotaside hook claude 2>/dev/null; echo "exit=$?"
# {"decision":"block","reason":"✓ Jotted #1","hookSpecificOutput":{"hookEventName":"UserPromptSubmit","suppressOriginalPrompt":true}}
# exit=2
```

A block exits 2 and repeats the reason on stderr, so Claude Code still blocks
the jot if it ever rejects the JSON.

## Cursor hook

Cursor is not supported (see [cursor.md](cursor.md)); this only checks the hook's replies.

```sh
bin/jotaside hook cursor < testdata/hooks/cursor/normal.json      # {"continue":true}
bin/jotaside hook cursor < testdata/hooks/cursor/malformed.json   # {"continue":true}
jq --arg r "$PWD" '.workspace_roots=[$r]' testdata/hooks/cursor/capture_single_root.json | bin/jotaside hook cursor
# {"continue":false,"user_message":"✓ Jotted #2"}
bin/jotaside hook cursor < testdata/hooks/cursor/capture_multi_root.json
# {"continue":false,"user_message":"✓ Jotted #3"}   (roots in metadata, no git fields)
```

## Codex hook

Ordinary prompts, mid-prompt `>>` and malformed payloads must print **nothing**
(Codex adds hook stdout to the model's context):

```sh
for f in normal mid_prompt malformed; do
  printf '%s: [' $f; bin/jotaside hook codex < testdata/hooks/codex/$f.json; echo "] exit=$?"
done
# normal: [] exit=0
# mid_prompt: [] exit=0
# malformed: [] exit=0
jq --arg cwd "$PWD" '.cwd=$cwd' testdata/hooks/codex/capture.json | bin/jotaside hook codex 2>/dev/null; echo "exit=$?"
# {"decision":"block","reason":"✓ Jotted #4"}
# exit=2
```

Like the Claude Code hook, a block exits 2 with the reason on stderr; Codex
shows the stderr copy.

## Failure is safe

```sh
JOT_DB=/dev/null/nope.db bin/jotaside hook claude < testdata/hooks/claude/capture.json 2>/dev/null; echo "exit=$?"
# {"decision":"block","reason":"✗ Jot NOT saved (...). Your text:\n\n>> token cache ...","hookSpecificOutput":{"hookEventName":"UserPromptSubmit","suppressOriginalPrompt":true}}
# exit=2
```

## Read back

```sh
bin/jotaside inbox
bin/jotaside show 1
bin/jotaside search cursor
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"inbox","arguments":{}}}' \
  | bin/jotaside mcp
```

## Find and sessions

Use a fresh database so the jot numbers match. The Claude session starts in
`start` and the agent then moves into `start/wt`; the transcript records both,
and the resume command must `cd` to `start`. The folder name has a space and
an apostrophe to check quoting.

```sh
export JOT_DB="$(mktemp -d)/jot.db"
start="$(dirname "$JOT_DB")/proj dir's"; mkdir -p "$start/wt"
tr="$(dirname "$JOT_DB")/c1.jsonl"
jq -nc --arg c "$start" '{type:"user",cwd:$c}' > "$tr"
jq -nc --arg c "$start/wt" '{type:"user",cwd:$c}' >> "$tr"
jot() { jq -c --arg s "$2" --arg c "$3" --arg p "$4" --arg t "$tr" \
  '.session_id=$s | .cwd=$c | .prompt=$p | .transcript_path=$t' \
  testdata/hooks/$1/capture.json | bin/jotaside hook $1 >/dev/null; }
jot claude c1 "$start/wt" ">> session: SUP-4821 token TTL"
jot claude c1 "$start/wt" ">> SUP-4821 needs a backend ticket"
jot codex x1 /tmp/deleted ">> SUP-4821 retry in codex"
bin/jotaside add "SUP-4821 from the terminal" >/dev/null
bin/jotaside done 2 >/dev/null
bin/jotaside find sup-4821
```

Expected, newest session first. Jot #2 is marked `(done)`, Codex has no `cd`,
and the Claude `cd` is the starting folder, not `wt`:

```
SUP-4821 retry in codex
  codex · 1 jot · last today (#3)
  #3    today  SUP-4821 retry in codex
  codex resume x1

SUP-4821 token TTL
  claude · 2 jots · last today (#2)
  #1    today  session: SUP-4821 token TTL
  #2    today  SUP-4821 needs a backend ticket  (done)
  cd '/…/proj dir'\''s' && claude --resume c1

Not in a session:
#4    …  SUP-4821 from the terminal  […]
```

Then check that the printed `cd` works, and the other commands:

```sh
sh -c "$(bin/jotaside sessions | grep 'claude --resume' | sed 's/ && claude.*/ \&\& pwd/')"
# /…/proj dir's
bin/jotaside sessions -n 1     # only the Codex session
bin/jotaside find nope         # No jots match "nope".
bin/jotaside find "  "         # jotaside: empty search query (exit 1)
```

Against your real database (read-only), check that each printed resume command
points at a session that exists: Claude Code sessions are
`~/.claude/projects/<start folder, non-alphanumerics as ->/<id>.jsonl`, and
Codex sessions are under `~/.codex/sessions`.

## Latency of the non-match path

```sh
time (for i in $(seq 200); do bin/jotaside hook claude < testdata/hooks/claude/normal.json; done)
```

Divide by 200. The non-match path never opens SQLite or runs git, so this is
essentially process start-up.

## In the real tools

1. Install the hooks as described in the README.
2. In Claude Code, type `>> hello from claude`. You should see `✓ Jotted #N`,
   and the model should not respond.
3. In Codex, trust the hook in `/hooks` first, then type `>> hello from codex`.
   You should see the same, and Codex should not start a turn.
4. In each tool, type `what does >> do in bash?`. It must go to the model as
   normal.
5. `jotaside inbox` shows the two jots with repo and branch.

Cursor is not supported (see [cursor.md](cursor.md)), so it has no real-tool step. The
`jotaside hook cursor` replays above still check the hook itself.
