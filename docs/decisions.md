# Decisions

Why aside is the way it is. Each entry says what was decided, why, and what
would make it worth revisiting. Add an entry when a choice is likely to be
questioned again; don't rewrite old ones, add a new entry that replaces them.

## Not in v1, on purpose

**Decided:** v1 leaves these out:

- **Triage**: no priorities, tags, snoozing or workflows beyond `inbox`/`done`.
- **Promote beyond GitHub**: `aside promote` creates GitHub issues only, on
  request; no TODOs, Jira tickets or automatic promotion.
- **Classification**: no automatic categorising or summarising, and no LLM in
  the loop.
- **Sync**: local SQLite file only; use `aside backup` to copy it.
- **UI**: CLI and MCP only.
- **Cursor**: not supported until Cursor stops sending blocked prompts to the
  model; see [cursor.md](cursor.md).

**Why:** the MVP proves one thing: capturing is instant, reliable and
invisible to the model. Everything else can come later.

**Revisit when:** real use shows one of these is missing often enough to be
worth its weight.

## One tool for every agent, not one per agent

**Decided:** a single binary with a small adapter per agent
(`internal/hooks/<agent>.go`), not separate Claude Code, Codex and Cursor
projects.

**Why:** the capture rule, storage, git context, CLI and MCP server are the
same for every agent; only the hook payload and reply format differ, and those
are a few dozen lines each. Separate projects would triplicate the parts that
matter and drift apart. Agent-specific behaviour, such as Cursor running Claude
Code hooks, is handled where the adapters meet, which separate tools couldn't
see.

**Revisit if:** an agent's hook model diverges so far that its adapter
dominates the codebase.

## Cursor is parked until Cursor fixes its bug

**Decided:** Cursor is documented as not supported. `aside hook cursor` stays in
the binary and its tests stay green, but nothing new is built for Cursor.

**Why:** Cursor keeps a blocked prompt in the chat and sends it to the model
with the next message ([#25](https://github.com/kacxx/aside-jot/issues/25)).
Cursor has reproduced it and is tracking it, with no date for a fix. A hook
only decides whether a prompt is sent now, so aside can't work around it, and
"the model never sees a jot" is the product's main promise. Cursor's own
workaround (edit the blocked message and resend) was verified on Cursor
3.23.12, but it's manual and easy to forget.

**Revisit when:** Cursor ships a fix. The hook replays in
[SMOKE_TEST.md](SMOKE_TEST.md) only check the hook's JSON and stay green
while the bug exists, so they can't confirm a fix. Confirm it in Cursor on
that version, the way the edit-and-resend check in [cursor.md](cursor.md) was done on 3.23.12:
in one chat, jot `>> ` with a word that appears nowhere else, send an ordinary
message, then ask the model to quote every earlier message. Cursor is fixed
only if the word never reaches the model.

## The MCP server is read-only

**Decided:** the MCP server offers read tools only (at first `inbox`, `show`, `search`
and `find`; later `sessions` and `activity`, see below). Agents
can't create, edit, close or delete jots.

**Why:** jots are the user's notes. An agent that tidies them silently can
lose something the user meant to keep, and a read-only server can't be
prompt-injected into doing that. Capture already has a reliable path (the
hook), and closing a jot is one command.

**Cost:** an agent triaging the inbox can only tell the user which jots to
close. If that friction matters, prefer making the CLI faster to use (for
example, `aside done` taking several ids) over giving the server write access.

**Revisit if:** there's a write that's safe to do without asking, or a
confirmation step the agent can't bypass.

The tool list grew in
[MCP sessions and older_than_days stay read-only](#mcp-sessions-and-older_than_days-stay-read-only)
and [activity returns evidence, not a verdict](#activity-returns-evidence-not-a-verdict).

What "read-only" guarantees was narrowed by
[Read-only covers the MCP server, not the agent](#read-only-covers-the-mcp-server-not-the-agent).

## Read-only covers the MCP server, not the agent

**Decided:** the MCP server stays read-only, but aside doesn't claim more than
that. An agent with shell access can run `aside` commands, including
`aside done` and `aside promote`, and nothing in aside prevents it. The guard
on those is the agent's own command approval, not aside, and in auto-approve
modes that approval is a classifier or nothing at all.

`promote` is the riskier of the two. A closed jot stays in the database and
in `aside search`; a promoted one is a GitHub issue, possibly in a public
repo. Because aside runs `gh` itself, an approval rule that allows `aside` but
gates `gh` doesn't catch it.

**Why:** in real use (5 Oct 2026), a Claude Code agent was asked to close
finished jots, found the MCP tools couldn't, and ran `aside done` from the
shell instead. That was what the user asked for, but it showed the earlier
entry's claim, that a read-only server can't be prompt-injected into tidying
jots, only holds for the server. Saying "agents can't close jots" would
mislead users about what protects their notes.

**So:** the MCP server keeps offering no writes, so an agent without shell
access, or one whose shell commands need a person's approval, still can't
change or publish jots silently. Since closing jots on request is a real
workflow, make the CLI path easy to find and fast
([#43](https://github.com/kacxx/aside-jot/issues/43)) instead of pretending
it doesn't exist.

**Update ([#45](https://github.com/kacxx/aside-jot/issues/45)):** `promote`
now always shows the issue and asks `[y/N]`, and refuses to run when stdin is
not a terminal unless `--yes` is passed (`--dry-run` is unchanged). A plain pipe
can't answer, so an agent has to pass `--yes`, which the user or their approval
rules can see in the command. This stops accidental publishing, and only
covers a plain pipe: a pseudo-terminal (an agent shell that allocates one, or
`script`) can still answer the question, and an agent that deliberately passes
`--yes` still relies on command approval. The question goes to stderr so
`$(aside promote 12 --yes)` and pipes keep stdout clean.

**Revisit if:** jots are changed or promoted in ways users didn't ask for.

## Find and sessions shipped as a thin slice

**Decided:** `aside find` and `aside sessions` (PR #32) group existing jots by
the session id hooks already record. No new capture fields, tags or indexes.
The larger session-index design stays in
[#20](https://github.com/kacxx/aside-jot/issues/20) and
[#30](https://github.com/kacxx/aside-jot/issues/30).

**Why:** what's useful is easier to learn from daily use than to design up
front. The slice needed no schema change, so it's cheap to change or drop.

**Revisit after:** a few weeks of real use. Let what's missing in practice
decide what comes next from #20.

## Resume commands use where the session started

**Decided:** for Claude Code, the printed resume command `cd`s to the first
`cwd` in the session's transcript, falling back to the jot's `cwd` if the
transcript is gone. For Codex, there's no `cd`.

**Why:** Claude Code files a session under the directory it started in. A
jot's `cwd` is wherever the agent was at the time, which can be a worktree or
repo it moved into later; resuming from there can't find the session. Codex
looks sessions up globally, so a `cd` would only make the command fail if that
directory was deleted.

**Revisit if:** either agent changes how it stores or finds sessions.

## Open links rely on an undocumented Claude Desktop route

**Decided:** `open_url` (MCP `inbox`, `show`, `find`; `aside show`) is
`codex://threads/<id>` for Codex and, only when the session's entrypoint is
exactly `claude-desktop`, `claude://code/continue?session=local_<id>` for
Claude Desktop. The `continue` route is undocumented (found in Desktop
2.19675.0's URL handler and clicked by hand), so everything else falls back to
the resume command: other entrypoints (`cli`, `sdk-cli`, `claude-desktop-3p`),
archived sessions (the handler ignores them) and sessions whose file is gone.
The Desktop id is the one the hook stored, but it is only used after Desktop's
session file of that name confirms it belongs to the jot's session
(`cliSessionId`) and isn't archived. Jots without it are found by reading
the first 4 KB of the session files newest first, stopping when all are found
or at the first file modified before the oldest jot looked for (Desktop
rewrites a file as the chat goes on, so it can't predate a jot in it). A miss
is remembered for 10 minutes, and not at all for a jot captured in the last 10,
since Desktop may not have written the file yet; a stored id is checked before
the remembered misses. No persistent cache, no schema change.
Ids are checked against a UUID (or `local_` UUID) shape before they go in a
link.

**Why:** most jots come from Claude Desktop and there is no documented way back
to a session. Reading only the header of Desktop's files keeps the lookup small
and avoids its settings. A wrong or stale link is worse than none, so every
doubt means no link.

**Revisit if:** Desktop changes or removes the route or its session files, or
documents a link of its own. Windows and Linux Desktop paths are not checked, so
there is no Desktop link there. Codex CLI-started threads and VS Code are untested.

Lookup time, for a jot whose Desktop session file is gone, on a Mac with 1,399
session files (Desktop 2.19675.0, measured on aside `f75bc6f`):

| Jot age | Cold cache | Warm |
| --- | --- | --- |
| 6 days | 0.69 s | 0.04 s |
| 60 days | 4.07 s | 0.13 to 0.16 s |
| Before the scan stopped at the jot's age, any age | about 7 s | 0.18 s |

Jots whose session file exists cost 20 to 50 ms per `aside show`. The saving
depends on the jot's age: 171 of the files were modified in the last week and
477 in the last 30 days.

How the link is used was replaced by "Open links are opened with `aside open`,
not clicked" below.

## Open links are opened with `aside open`, not clicked

**Decided:** `aside open <id>` opens a jot's chat link with `open` on macOS or `rundll32` on Windows, and
the MCP tool descriptions tell the agent to offer that command (or run it when
asked) instead of showing `open_url` as a link. It passes the system only a
Claude Desktop `continue` link or a Codex thread link, each with a UUID id,
after rebuilding the link the way `aside show` does, so a stored value can't
make it open anything else. A jot with no link gets a message and its resume
command. Other platforms get a message saying it is unsupported.

**Why:** on Desktop 2.19675.0 (macOS, 2026-10-06) clicking `open_url` did not
open the chat in any form tried: a markdown link, a bare URL and a link in a
table cell all just selected the text and showed Desktop's "Send to side chat /
Reply" popover. An earlier hand test had opened the link, so clicking isn't a
reliable path. Desktop's agent can run shell commands and offers a command
block with a Run button, so a command works for everyone, without per-user
settings.

**Not changed:** the MCP server is still read-only. `aside open` is one more
command an agent with shell access can run, guarded only by its command
approval, like `aside done`. It changes no jot and only focuses a chat.

**Per session:** the link belongs to the chat, not the jot. It is built from what
all of the session's jots record, so a jot without the Desktop id (one from
before it was stored) still opens its session's chat, and `find` can name any
jot of a session in its command. `find` and `sessions` look links up only for the
sessions they show, after the limit is applied.

**Full path:** agents run it from a shell that may not have aside on its `PATH`
(on one Mac, `~/go/bin` was on neither a login nor a non-interactive shell's), so
the MCP tools return `open_command` with the full path to aside, from
`os.Executable`, next to each `open_url`, and the descriptions tell the agent
to offer it as given. Off macOS and Windows, where `aside open` can't run, no `open_command`
is offered (`open_url` still is).

**Verified:** on a Mac with Claude Desktop 2.19675.0, `aside open <id>` for a
Desktop jot brought Desktop forward on that jot's own session. Not verified:
`aside open` for a Codex app jot on a Mac. Run by a Codex CLI v0.160.1 agent on
the Mac (2026-10-07), `aside open` failed with "No application knows how to open
URL … kLSExecutableIncorrectFormat", while the same command in a plain Terminal
worked, so an agent's shell can be unable to open links even when the user's
can; the Codex sandbox is the likely cause, unconfirmed.

**Codex links on Windows** are kept, on three results from Windows 10 (build
19045):
- 2026-10-05, Codex app 26.930.31730 (build 12947): opening
  `codex://threads/<id>` from a different chat switched to the right thread (#44).
- 2026-10-07, app version not recorded, app already running: neither
  `aside open` nor `Start-Process "codex://threads/<id>"` in plain PowerShell
  brought the app forward. aside still printed "opened", as it does for any
  link `rundll32` hands off.
- 2026-10-08, app version not recorded, app closed: `aside open` started the
  Codex app on the jot's thread (#61).

The likely cause of the 10-07 failure is the running app: it may not open a
thread created while it was running until it restarts, or Windows may not
let a background launch raise its window. Neither was confirmed. The docs tell
users to restart the Codex app if a link doesn't switch the thread. #58, which
removed Codex links on Windows, was closed instead of merged.

**Windows:** Desktop 2.19675.1 on Windows 10 (build 19045, 2026-10-07) keeps its
session files in `%APPDATA%\Claude\claude-code-sessions\<account>\<org>\local_<uuid>.json`
(the MSIX package mirrors them under `%LOCALAPPDATA%\Packages\Claude_*`), with
`cliSessionId` and `isArchived` in the first 1.2 KB as on the Mac, and `cliSessionId`
equals the hook's session id. The `claude://code/continue?session=local_<id>` link
switched Desktop to the right session when run from PowerShell, and
`aside open 7` (build 78bdced) printed the same link, found by the lookup, and
opened it. `rundll32 url.dll,FileProtocolHandler` reports no failure, so a link
nothing handles prints "opened". Not checked on Windows: whether Desktop sets
`CLAUDE_CODE_HOST_SESSION_ID` there, so the lookup is what was tested. The hook on
that PC pointed at an old `jot.exe` until it was changed to `aside.exe`, which is
why no Desktop jot had the id.

**Revisit if:** Desktop makes links in chat clickable, or documents a link of
its own. `aside open` on Linux isn't built: there is no Claude Desktop for it that
we know of. For Codex on Windows: if a Codex link fails again with the app
freshly restarted, note the app version and reconsider #58.

## Promote only creates GitHub issues, on request

**Decided:** `aside promote <id>` creates one GitHub issue through `gh`, only
when asked, with `--dry-run` to preview it. No other trackers and no automatic
promotion.

**Why:** it keeps the "no LLM in the loop, nothing automatic" promise and
reuses the user's existing `gh` authentication instead of storing tokens.
Including the agent's reply in the issue
([#33](https://github.com/kacxx/aside-jot/issues/33)) is opt-in for the same
reason: transcripts can contain secrets and the target repo may be public.

## Ticket keys are matched at query time, not tagged

**Decided:** `aside find SUP-4821` runs the substring search, then keeps the
jots where the key appears as a whole word. There is no `jot_tags` table, no
prefix allowlist and no backfill. The MCP server gets a read-only `find` with
the same lookup.

**Why:** the problem to solve was `SUP-4821` also finding `SUP-48210`. Tags
written at capture time depend on the environment of whichever process captured
the jot (hooks in GUI editors run with a minimal one), so a jot captured
without the allowlist would be untagged and `find` would silently miss it,
which substring search never did. A query-time filter has nothing to drift out
of sync, needs no migration, and the data is small enough that an index buys
nothing noticeable. A plain `[A-Z]+-\d+` pattern would also match `UTF-8`, which
only matters when picking keys out of text; a user asking for `UTF-8` gets
whole-word matches of it.

**Revisit if:** `find` needs to answer questions the text can't, such as "all
ticket keys in this session" or listing tickets by count. Then add `jot_tags`
with the allowlist in a config file next to the database, so every process
sees the same one.

## `aside setup` writes the path as invoked and doesn't run anything else

**Decided:** `aside setup claude|codex` merges the hook into the agent's JSON
config. It writes the binary's path as it was invoked (made absolute, symlinks
kept), refuses a temporary binary, keeps the file's key order, backs it up
before every change and treats `jot hook` entries as aside's own. `--mcp` only
prints the `claude mcp add` / `codex mcp add` command.

**Why:** a resolved path breaks when the tool is upgraded through a symlink
(`os.Executable` resolves it on Linux), a `go run` path disappears, and
decoding into Go maps would sort and reformat a file the user keeps by hand.
Running `mcp add` would change another tool's config behind `--dry-run`'s back,
which is what the no-surprises rule (ask before editing hook configs) is
against.

**Revisit when:** an agent offers an install or registration command of its
own that aside can call safely.

## MCP sessions and older_than_days stay read-only

**Decided:** the MCP server gains a `sessions` tool and an `older_than_days`
argument on `inbox` (2026-10-08). Both read what `aside sessions` and
`aside inbox --older` already show, so the server is still read-only. There is
still no capture tool, and the docs tell users to jot with `>> ` rather than
ask the agent to run `aside add`.

**Why:** without them an agent couldn't answer "what was I working on?" or
"anything older than a week?" except by running the CLI. Asking an agent to "jot this" sends the note
through the model, which is what a `>> ` jot exists to avoid.

**Revisit if:** an agent needs a write the CLI can't do; see
[The MCP server is read-only](#the-mcp-server-is-read-only).

## activity returns evidence, not a verdict

**Decided:** `aside activity <id>` and the read-only MCP tool `activity` return
what a jot's own session did after the jot: prompts, agent text and collapsed
tool calls, oldest first, within a size cap (2026-10-14, #78, #82, #84). They
say nothing about whether the jot was done. An agent reads the records and
judges, and the user approves any `aside done` it proposes.

**Why:** "was this actioned?" is a judgement on free text (a jot reading "get
code reviews" and a session that says "I've added review comments" share no
words), which needs a model, and aside has none. Giving the agent the evidence
keeps aside a plain store. The text is the transcript of a past session, which
can hold tool output and messages from other sources, so the tool says it is
data, not instructions.

**Cost:** the cap keeps the oldest records after the jot, so a long session
shows its start, not its end. Only Claude Code and Claude Desktop jots can be
read; Codex jots store their transcript path but the rollout format is not
read yet.

**Revisit if:** the oldest-first cap hides the evidence in real use (add a tail
option), or the evidence proves clear enough that closing the obvious cases
without asking is worth the risk (see
[Read-only covers the MCP server, not the agent](#read-only-covers-the-mcp-server-not-the-agent)).

## Public repos need `--public` under `--yes`

**Decided:** before creating an issue, `aside promote` asks `gh repo view`
for the target repo's visibility. A public repo gets a warning line in the
preview, just before `[y/N]`, and in `--dry-run` output. With `--yes`, which
skips the preview, a public repo is refused unless `--public` is also passed.
If the lookup fails, the repo is treated as possibly public: the preview says
so, and `--yes` needs `--public` too. Private and internal repos are
unchanged. A dry run now runs `gh`, but only that read.

**Why:** a jot is a private note, and `--with-reply` can add transcript text
with secrets or internal paths. Asking first
([Read-only covers the MCP server, not the agent](#read-only-covers-the-mcp-server-not-the-agent))
shows the person what will be posted, but not who can read it: the preview
looked the same for a private repo and a public one. Under `--yes` no
one sees a preview, and an agent usually has to pass `--yes`, so a public
issue could be published with nothing in the command saying it would be
public. `--public` makes that choice visible in the command, where command
approval can see it. Failing safe on an unknown visibility follows the same
rule as the rest of promote: when in doubt, don't publish.

**Verified:** on gh 2.92.0 (2026-10-24), `gh repo view OWNER/NAME --json
visibility` and the `github.com/OWNER/NAME` form both print
`{"visibility":"PUBLIC"}` for a public repo, and a missing repo exits 1 with
`GraphQL: Could not resolve to a Repository …`. The promote flow itself was
checked with a stand-in `gh`, not by creating real issues; GitHub Enterprise
was not checked.

**Revisit if:** `gh` stops reporting visibility for a host people use (every
`--yes` there would then need `--public`), or GitHub adds a visibility a
public check doesn't cover.
