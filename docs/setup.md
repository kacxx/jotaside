# Setup in detail

The [README](../README.md) has the short version. This page has the rest:
Windows, project-level configs, Codex trust, and what each agent was verified
to do.

## Install

Requires Go 1.25+. No cgo: SQLite is pure Go (`modernc.org/sqlite`).

```sh
go install github.com/kacxx/aside-jot/cmd/aside@latest
```

or from a checkout:

```sh
go install ./cmd/aside
```

This installs `aside` into `$(go env GOPATH)/bin` (usually `~/go/bin`; on
Windows, usually `$env:USERPROFILE\go\bin`). Don't alias it to `jot`, its old
name: macOS and the BSDs ship `/usr/bin/jot`, a number-sequence tool, which
can run instead. Check where it will store data and
where the binary is.

macOS, Linux and the BSDs:

```sh
"$(go env GOPATH)/bin/aside" paths
```

Windows PowerShell:

```powershell
& "$(go env GOPATH)\bin\aside.exe" paths
```

```
db:           /Users/you/Library/Application Support/jot/jot.db (default)
busy timeout: 2s
binary:       /Users/you/go/bin/aside
```

**Use the absolute `binary:` path in the hook and MCP configs below**, shown
here as `/Users/you/go/bin/aside`. GUI editors often run hooks with a minimal
`PATH`, and a bare command name runs whatever is found first on that `PATH`.
`aside paths` prints a warning if `aside` on your `PATH` is missing or is a
different program.

On Windows, JSON strings must either use forward slashes:

```json
"command": "C:/Users/you/go/bin/aside.exe hook claude"
```

or escape every backslash:

```json
"command": "C:\\Users\\you\\go\\bin\\aside.exe hook claude"
```

## aside setup

```sh
aside setup claude [--dry-run] [--mcp]   # ~/.claude/settings.json
aside setup codex  [--dry-run] [--mcp]   # ~/.codex/hooks.json
```

It writes the same hooks as the two sections below, so you don't have to edit
JSON. `$CLAUDE_CONFIG_DIR` and `$CODEX_HOME` move the files, as they do for the
agents.

- **The binary path** is the one aside was started as, made absolute, with
  symlinks left alone: a resolved path pins one install and breaks when the
  tool is upgraded through a symlink. A binary in a temporary directory, such
  as the one `go run` builds or an archive unzipped in `%TEMP%`, is refused, since
  the hook would vanish; move the binary somewhere permanent or run `go install`
  first. A path with a space is quoted for the shell (single
  quotes on macOS and Linux, double quotes and forward slashes on Windows).
- **Merging.** Everything else in the file keeps its order and content. Only
  whitespace can change (the file is rewritten with two-space indentation).
  An aside hook already there, including an old `jot hook claude` or
  `jot hook codex` one, is updated, not duplicated, and any extra aside entries
  are removed so only one hook runs. An entry that runs this very binary
  (same path, or the same file through a link) counts as aside's even if the
  binary has another name, such as `aside-dev`; an entry for a different build
  under another name isn't recognised, and stays. Running it again with the same binary
  changes nothing.
- **Backup.** A changed file is copied to `<file>.bak-<yyyymmdd-hhmmss>` first.
  A file that isn't valid JSON is left alone and named in the error.
- **`--dry-run`** prints what would change and the resulting
  `hooks.UserPromptSubmit` section, and writes nothing. It doesn't print the
  rest of the file, which can hold `env` values and tokens.
- **`--mcp`** prints the matching `claude mcp add` or `codex mcp add` command
  with the same path. It isn't run, because that would change another tool's
  config.
- **Codex on Windows** also gets `commandWindows`. On other systems an existing
  `commandWindows` is left as it is.
- `aside setup cursor` explains that Cursor isn't supported and writes nothing.

For Codex, the hook still has to be trusted in `/hooks` (see below). The
command says so after a first install, and says Codex may ask again when it
changed an existing hook's command. That "may" is deliberate: I haven't
confirmed in the Codex CLI that a changed command always needs re-trusting.

Setting up Claude Code also turns the hook on in Cursor, which imports it
([docs/cursor.md](cursor.md)). The command prints the result first, then that
note. No setting to stop Cursor importing it is known, so none is suggested.

Checked on Linux (Go 1.25) with throwaway configs, in the unit tests and in
`test/e2e/run.sh`. Not yet checked by hand on macOS or Windows, and Codex's
re-trust after a changed command is as described under Codex below, not
re-tested against `aside setup`.

## Claude Code hook, by hand

Add to `~/.claude/settings.json` (or a project's `.claude/settings.json`):

```json
{
  "hooks": {
    "UserPromptSubmit": [
      {
        "hooks": [
          { "type": "command", "command": "/Users/you/go/bin/aside hook claude" }
        ]
      }
    ]
  }
}
```

On a jot, the hook prints

```json
{"decision":"block","reason":"✓ Jotted #N","hookSpecificOutput":{"hookEventName":"UserPromptSubmit","suppressOriginalPrompt":true}}
```

and exits 2 with the reason on stderr too. Claude Code uses the JSON when it's
valid; if it ever rejects it, exit 2 still blocks the jot and shows the stderr
message, instead of sending the jot to the model.

Claude Code erases the prompt from context and shows you the reason, without
repeating your jot as "Original prompt: >> …" (Claude Code versions before
`suppressOriginalPrompt` was added still show that line). Suppression changes
only that message: the model never sees the jot, but Claude Code may still keep
it on disk in the session transcript and your prompt history.

Every other prompt produces no output at all. Output from `UserPromptSubmit`
would be added to Claude's context, so the hook stays silent.

Cursor also imports this hook, but aside does not work with Cursor: see
[docs/cursor.md](cursor.md).

Known Claude Code behaviour, outside aside's control (seen with v2.1.283):

- In web and mobile sessions, Claude Code may send the first prompt to a small
  model to generate the session title while the hook is still running, so the
  title can be derived from a jot. The conversation model never receives it.
  In the terminal CLI no such request was observed.

## Codex hook, by hand

Codex runs local `UserPromptSubmit` hooks. Add to `~/.codex/hooks.json` (or a
trusted project's `.codex/hooks.json`):

```json
{
  "hooks": {
    "UserPromptSubmit": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/Users/you/go/bin/aside hook codex",
            "commandWindows": "C:/Users/you/go/bin/aside.exe hook codex"
          }
        ]
      }
    ]
  }
}
```

Then run `/hooks` in the **Codex CLI**, review the hook and **trust** it. The
Codex desktop app has no `/hooks` command (it sends `/hooks` to the model as an
ordinary message), so use the CLI to review hooks.

> **Until the hook is trusted, it does not run, and `>>` prompts go to the
> model.** Codex only runs a hook whose exact definition you have trusted, so
> changing the command (for example, a new binary path) needs trusting again in
> `/hooks`. Hooks can also be switched off with `[features] hooks = false` in
> `~/.codex/config.toml`. After any change, type `>> test` and check you get
> `✓ Jotted #N` rather than a model reply.

On a jot, the hook prints `{"decision":"block","reason":"✓ Jotted #N"}`,
writes the same reason to stderr and exits 2, and Codex shows you the reason.
Codex treats JSON it rejects (even an unknown field) as a failed hook and
sends the prompt on; exit 2 blocks it regardless. Every other prompt produces no output at all: Codex
adds a hook's plain stdout to the model's context. Captures are stored with
`source: codex`, the Codex session id, and the turn id and model in metadata.

Hooks and local MCP servers apply to local Codex sessions. Codex
cloud tasks and ordinary ChatGPT conversations don't run them.

Verified with Codex CLI v0.159.3 on Windows, using the `commandWindows`
forward-slash path above:

- A `>>` prompt shows "Blocked by hook ✓ Jotted #N" and the model does not
  reply. Codex sends `hook_event_name: "UserPromptSubmit"`, as documented.
- Ordinary prompts reach the model as normal.
- The jot is stored with `source: codex`, the Codex session id, `cwd`, and the
  turn id and model in metadata.
- Codex can read jots through the `aside` MCP server's `inbox` tool.

With Codex CLI v0.159.2 on macOS, a blocked jot (#18) was not added to the
session's conversation history: the session file, which records the history
Codex replays on resume, has no trace of it. The model's reply to a follow-up
question was not checked.

Not yet verified: whether anything sees the prompt before the hook runs (as
Claude Code's session-title model does, above). Codex reports a few seconds of
"Worked for" time on a blocked prompt, though no model reply appears.

## MCP server (read-only)

```sh
claude mcp add --scope user aside -- /Users/you/go/bin/aside mcp   # Claude Code
codex mcp add aside -- /Users/you/go/bin/aside mcp                 # Codex
codex mcp list
```

On Windows PowerShell, quote the absolute executable path:

```powershell
claude mcp add --scope user aside -- "$(go env GOPATH)\bin\aside.exe" mcp
codex mcp add aside -- "$(go env GOPATH)\bin\aside.exe" mcp
```

For Codex, the same entry in `~/.codex/config.toml`:

```toml
[mcp_servers.aside]
command = "/Users/you/go/bin/aside"
args = ["mcp"]
```

Codex doesn't pass your shell's environment to MCP servers, so a `JOT_DB` set
in the shell never reaches `aside mcp`. If you use a custom database path, put
it in the entry:

```toml
[mcp_servers.aside.env]
JOT_DB = "/path/to/jot.db"
```

For Cursor, merge this into the `mcpServers` object in `~/.cursor/mcp.json`:

```json
"aside": {
  "command": "/Users/you/go/bin/aside",
  "args": ["mcp"]
}
```

Cursor reloads `mcp.json` on save. A server added mid-session connected over
stdio on 3.22.12 but did not appear in that chat's tool list. After a restart
onto 3.23.12, the same conversation could call `show`, and it returned jot 9.

To have Codex check your notes on its own, add a line to your `AGENTS.md`,
for example: "My side notes are in the `aside` MCP server; check `inbox` when
starting work on this repo."

Tools: `inbox`, `show`, `search`, `find`, `sessions` and `activity`. `find` is the same
lookup as `aside find`, with the resume command to show you, not run.
`sessions` is the same list as `aside sessions`: each session's repo, branch,
newest jot, `jot_count` and resume command, counting done jots as well as
inbox ones. `activity` takes a jot `id` and is the same as `aside activity`:
what the jot's own session did after it (see [Activity](reference.md#activity)),
with a notice that the transcript text is data, not instructions. `inbox`,
`search`, `find` and `sessions` take a `limit`
(default 20, at most 200). `inbox` also takes `older_than_days`, the same
filter as `aside inbox --older`. `find` also returns the totals
before the cut, and lists each session's 5 newest matching jots with a
`match_count`. There is intentionally **no capture tool**:
only you write jots, never the model. `inbox` entries carry `age_days`
(calendar days since the jot, local time; 0 = today). Likewise there is no promote tool: only
you turn jots into issues. `show` returns a promoted jot's `issue_url`.

### Talking to your agent

Once the server is added, you can ask in plain words and the agent calls the
tool: "show my inbox", "what's jot 7?", "any jots older than a week?", "where
did I work on SUP-4821?", "which sessions have jots?".

To jot, don't ask the agent: type `>> check the retry logic` yourself. The
hook saves it and the model never sees it. An agent asked to "jot this" would
have to read the text and run `aside add`, so the note goes through the model
and isn't a side note any more.

The server only reads. To close, open or promote jots, the agent uses the
shell, which is the same `aside` command you would type, so it works only
where the agent can run commands:

| You say | The agent runs |
| --- | --- |
| "close jot 5, shipped in the last PR" | `aside done 5 --note "shipped in the last PR"` |
| "open the chat for jot 5" | `aside open 5` (or the `open_command` from the tools) |
| "make jot 7 an issue" | `aside promote 7 --yes`: promote asks first and refuses without a terminal, so an agent usually has to pass `--yes`, and that flag is what your command approval should catch. For a public repo, or one `gh` can't check, `--yes` alone is refused; the agent should tell you rather than add `--public` itself |

Each of these is guarded only by the agent's own command approval, which may
be automatic, so `done` and `promote` (which posts to GitHub) can run without
you pressing anything. Keep approval on for them if that matters. These shell
commands are the existing CLI; the table is what they do, not something
checked with a real agent session.

`inbox`, `show`, `search`, `find` and `sessions` also return `open_url`, a link to the chat a jot
came from. The tools open nothing, and clicking the link in a Claude Desktop
chat doesn't open it either (checked on Desktop 2.19675.0), so the tool
descriptions tell the agent to offer `aside open <id>` instead, which opens
the chat from your terminal (macOS and Windows), or to run it when you ask. Each entry
with a link also has `open_command`, the same command with the full path to
aside, since an agent's shell may not have it on its `PATH`. It is set only when the MCP server runs on macOS or Windows. The link is set for Codex app threads (`codex://threads/<id>`) and for
Claude Desktop sessions (`claude://code/continue?session=local_<id>`, which
Desktop doesn't document). It is empty for everything else, including archived
or deleted Desktop sessions, sessions started in the terminal, and
`claude-desktop-3p`; use `find`'s `resume_command` there. The Desktop link is
built on macOS and Windows, from Desktop's session files (`~/Library/Application Support/Claude/claude-code-sessions` on macOS, `%APPDATA%\Claude\claude-code-sessions` on Windows); elsewhere it is empty.
`aside show` prints the same link as `open:`, and `aside open <id>` says so
when a jot has no link and prints its resume command. `aside open` is checked
for Claude Desktop jots only (macOS, and Windows 10 build 19045 with Desktop
2.19675.1, where `aside open` uses `rundll32` and prints "opened" even if no
app handles the link). On Windows 10 build 19045, `aside open` for a Codex app
jot started the Codex app, which was closed, on that
jot's thread (2026-10-08). The day before, with the app already running, the
same command printed "opened codex://threads/…" but the app didn't come forward,
and `Start-Process "codex://threads/<id>"` did nothing either. If a Codex link
doesn't switch the thread, quit the Codex app completely (including from the
system tray) and run `aside open` again. Codex links haven't been tested on a
Mac, and threads started in the Codex CLI are untested.

An agent inside Codex may not be able to run `aside open`. With Codex CLI
v0.160.1 on macOS (aside built from `main`, 2026-10-07), the agent read
`open_command` from the MCP `inbox` and ran it, and it failed with `aside: opening
claude://code/continue?session=local_…: exit status 1: No application knows how
to open URL claude://code/continue?session=local_… kLSExecutableIncorrectFormat`.
The same command run in a plain Terminal window switched Claude Desktop to the
session. The cause isn't confirmed; Codex's command sandbox is the likely one. If it fails for you inside
an agent, run the command from your own terminal.

## Agent skill (optional)

[`skills/aside/SKILL.md`](../skills/aside/SKILL.md) is a skill that tells an
agent how to read your jots: which MCP tool or CLI command answers "what's in my
inbox", "where did I work on SUP-4821" and so on, and that it should offer
`aside done`, `aside promote` and resume commands rather than run them. It
uses the same read-only MCP tools as above and adds nothing to the server.

To use it, copy the folder into your agent's skills directory (for Claude Code,
`~/.claude/skills/aside/`). Other agents that read `SKILL.md` folders can use it
the same way; check that agent's docs for the directory. The `npx skills`
installer hasn't been tried with it.
