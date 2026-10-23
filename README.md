# aside-jot

A side channel for thoughts while you work with coding agents.

Mid-task, you think of something unrelated: a bug to check, a ticket to raise,
a question for someone. Telling the agent derails it, and switching to a notes
app breaks your flow. With aside, you type it into the agent with `>>` in
front:

```
>> token cache TTL looks too long, check with infra before shipping
✓ Jotted #12
```

aside saves it with its git context (repo, branch, commit). In **Claude Code**
and **Codex** it also blocks the prompt, so the model never sees it and the
work in front of you keeps going. That is the don't-disrupt-my-flow feature.
It needs the hook to run: in Codex, it doesn't until you trust it in `/hooks`
([setup](docs/setup.md#codex-hook-by-hand)). And a jot sent while the agent is
busy can be queued and merged with other prompts, and then reach the model
([capture rules](docs/reference.md#capture-rules)).

**Cursor isn't supported.** Cursor keeps a blocked prompt in the chat and sends
it to the model with your next message, so a `>>` jot there still reaches the
model ([why](docs/cursor.md)). In Cursor, jot from a terminal with `aside add`
instead.

![Demo: a ">>" prompt is blocked and saved with its git context, Claude makes the change, "update jot 1" checks the session and closes the jot with a note, and the inbox still holds the other jot](docs/demo.gif)

*Recorded with the real Claude Code CLI (v2.1.309) and aside v0.7.0.
[`docs/demo/record.sh`](docs/demo/record.sh) re-records it.*

## Install

With Go 1.25+:

```sh
go install github.com/kacxx/aside-jot/cmd/aside@latest
aside setup claude    # or: aside setup codex
```

Without Go, download the archive for your system from the
[latest release](https://github.com/kacxx/aside-jot/releases/latest)
(macOS, Linux and Windows, amd64 and arm64). The files are
`aside_*_darwin_arm64.tar.gz`, `aside_*_darwin_amd64.tar.gz`,
`aside_*_linux_arm64.tar.gz`, `aside_*_linux_amd64.tar.gz`,
`aside_*_windows_arm64.zip` and `aside_*_windows_amd64.zip`. Put `aside`
somewhere it will stay, such as `~/.local/bin`, then run setup from there,
because setup records the path it was run from. On Apple Silicon:

```sh
shasum -a 256 -c checksums.txt --ignore-missing    # if you downloaded checksums.txt too
tar -xzf aside_*_darwin_arm64.tar.gz aside
mkdir -p ~/.local/bin && mv aside ~/.local/bin/
~/.local/bin/aside setup claude    # or: setup codex
```

On Linux the archive name changes and the rest is the same. On arm64 (use
`aside_*_linux_amd64.tar.gz` on x86_64):

```sh
sha256sum -c checksums.txt --ignore-missing    # if you downloaded checksums.txt too
tar -xzf aside_*_linux_arm64.tar.gz aside
mkdir -p ~/.local/bin && mv aside ~/.local/bin/
~/.local/bin/aside setup claude    # or: setup codex
```

`~/.local/bin` isn't on `PATH` in a new shell. Add
`export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc` on macOS, or to
`~/.bashrc` on Linux, and open a new shell to run plain `aside`.

The binaries aren't signed. On macOS, a file downloaded in a browser is marked
as quarantined, extracting it keeps the mark, and Gatekeeper may refuse to run
it; clear it with `xattr -d com.apple.quarantine ~/.local/bin/aside`. Downloads
with `curl` or `gh release download` aren't marked. On Windows, unzip
`aside_*_windows_amd64.zip` (or `aside_*_windows_arm64.zip`) and put
`aside.exe` in a folder where it will stay, then run
`.\aside.exe setup claude` from there in PowerShell.

`aside setup` adds the `UserPromptSubmit` hook to `~/.claude/settings.json`
(or `~/.codex/hooks.json`) with the binary's absolute path, since editors often
run hooks with a minimal `PATH`. It keeps your other settings and hooks, and
replaces an older aside entry instead of adding a second. When that file
already exists, setup copies it to `<file>.bak-<time>` first. When it does not
exist yet, the backup line says `none needed, the file did not exist`.
`--dry-run` shows the result and writes nothing; `--mcp` also prints the
command that gives the agent read-only access to your jots (it doesn't run
it). If `aside` isn't on your `PATH` yet, run it by its full path:
`"$(go env GOPATH)/bin/aside" setup claude` after `go install`, or
`~/.local/bin/aside setup claude` for a downloaded binary.

A first install, where the config file is new, prints the checkmark and then
a note that Cursor imports the same file:

```
$ aside setup claude
✓ Added the aside hook in /Users/you/.claude/settings.json
  command: /Users/you/.local/bin/aside hook claude
  backup:  none needed, the file did not exist
  Other settings and key order are kept; only whitespace may differ.

Note: Cursor imports hooks from ~/.claude/settings.json, so this also turns aside on in
Cursor, which isn't supported (see https://github.com/kacxx/aside-jot/blob/main/docs/cursor.md).
Use --dry-run to preview without writing.

Next: type `>> test` in Claude Code: you should see ✓ Jotted #N and no model reply.
```

For **Codex**, run `/hooks` in the **Codex CLI** afterwards and **trust** the
hook. Until it's trusted, `>>` prompts go to the model.

Prefer to edit the JSON yourself? See [docs/setup.md](docs/setup.md).

### Versions and updating

`@latest` installs the newest release; pin one with a tag such as `@v0.7.0`. What changed in
each release is in [CHANGELOG.md](CHANGELOG.md), and `aside version` prints the
one you have:

```
$ aside version
aside v0.7.0
```

To update, run the same `go install …@latest` again, or `mv` the new
downloaded binary over the old one at the same path (no need to rerun setup).
Use `mv` rather than `cp`: on Apple Silicon, copying over the old file can get
the new one killed on start. A browser download needs the `xattr` step again.
Hooks pick up the new binary straight away. The MCP server keeps running the old one until its
agent restarts, so restart Claude Desktop, Codex and any open agent sessions.

### Check it works

Type `>> test` in the agent. In Claude Code you should see
`UserPromptSubmit operation blocked by hook` above `✓ Jotted #N`, and no reply
from the model. That blocked line is Claude reporting that the hook stopped
the prompt; `✓ Jotted` means the jot was saved. Then run `aside inbox` in a
terminal. If your shell can't find `aside`, add `$(go env GOPATH)/bin` (after
`go install`) or the folder you put the downloaded binary in to your `PATH`.

Windows paths, project-level configs, Codex trust details and known agent
behaviour: [docs/setup.md](docs/setup.md).

### What aside keeps

The hook sees every prompt you send, but keeps only jots: prompts that start
with `>>`, a space and some text (the [capture rule](docs/reference.md#capture-rules)
has the details). Anything else passes through untouched and isn't stored.

For each jot, aside saves the text, the time, the agent and session it came
from, the folder, the git repo, branch and commit, and the session's transcript
path when the agent gives one. It all goes into one SQLite file on your machine
(`aside paths` shows where), readable only by you on macOS and Linux.

Some commands also read, on your machine, the transcript of the session a jot
came from: `activity` (and "update jot N") to see what the session did next,
`sessions` and `find` for the folder a chat started in, and `promote
--with-reply` for the agent's reply. `open` and `find` read Claude Desktop's
session list to link jots back to their chats.

aside makes no network connections. The one thing that sends anything anywhere
is `aside promote`, which posts to GitHub through your `gh` login, after asking.

### Uninstall

1. Remove the aside entry from the `UserPromptSubmit` hooks in
   `~/.claude/settings.json` (or `~/.codex/hooks.json`). `aside setup` backed
   the file up first (`<file>.bak-<time>`), but that copy is missing any
   changes you've made since.
2. Remove the MCP server, if you added it: `claude mcp remove --scope user aside`
   or `codex mcp remove aside`.
3. Remove the skill: `claude plugin uninstall aside@aside-jot` (and
   `claude plugin marketplace remove aside-jot`), or delete a copied
   `skills/aside` folder.
4. Delete the binary (`aside paths` shows where it is).
5. To delete your jots too, delete the database folder `aside paths` shows,
   after an `aside backup` if you might want them back.

## A day with aside

- **Morning.** Deep in a refactor, you jot three things without stopping:
  `>> SUP-4821 needs a backend ticket` (#21), `>> ask infra about the cache
  TTL` (#22) and `>> flaky test in billing, look later` (#23).
- **Lunch.** `aside inbox` lists them, newest first, with the repo and branch
  each came from. You fix the flaky test and run `aside done 23`.
- **Afternoon.** The ticket note deserves an issue. Since you jotted it in
  Claude Code, `aside promote 21 --with-reply` shows you an issue with your jot
  and what the agent had just said when you jotted it, and creates it in that
  repo when you say yes. (`promote` always asks first.)
- **Friday.** Someone asks about SUP-4821. `aside find SUP-4821` lists the
  chats where you jotted about it, with the command to reopen each one.

## Using aside

### Capture

| You type | What happens |
| --- | --- |
| `>> some thought` (in Claude Code or Codex) | Saved with the repo, branch, commit and chat; the model never sees it |
| `>> session: SUP-4821 token TTL` | Also labels the chat, so `aside find` and `aside sessions` show why it exists |
| `aside add some thought` (in a terminal) | Saved with the git context of the current directory |

Only a prompt that **starts** with `>>`, a space and then text is a jot (extra
spaces are fine). `>>x`, ` >> x` and `a >> b` go to the model as normal. To
send a prompt that starts with `>> `, put a space or a backslash in front.

In Claude Code or Codex, if a jot shows up as a chat message instead of
`✓ Jotted`, resend it once the agent is idle.

### Review

```sh
aside inbox                 # your open jots, newest first, with their age
aside inbox --older 7       # only the ones that have waited a week or more
aside show 12               # one jot with its full context
aside search ttl            # search every jot, done ones included
aside done 12               # mark it done; it drops out of the inbox
aside done 3 4 5 --note "pushed to Q4, see https://example.com/wiki/plan"   # several at once, with why
```

```
$ aside inbox
#14   today  SUP-4821 needs a backend ticket  [api@main] · SUP-4821 review
#12   2d     token cache TTL looks too long, check with infra before shipping  [api@main]
#11   2d     session: SUP-4821 token TTL investigation  [api@main]
```

When available, a Claude Code session's name follows the location (` · SUP-4821
review` above); aside reads it from the transcript when listing the inbox.

### Get back to a chat

Every jot made in an agent remembers its chat. `aside find` shows the chats
where you jotted about something, and prints the command to reopen each one:

```
$ aside find SUP-4821
SUP-4821 token TTL investigation
  claude · api@main · 2 jots · last 2d (#12)
  #11   2d     session: SUP-4821 token TTL investigation
  cd /Users/you/code/api && claude --resume 0f3c9a…
  aside open 12

Not in a session:
#14   2026-10-04 18:03  SUP-4821 needs a backend ticket  [api@main]
```

```sh
aside find SUP-4821         # chats that mention it (ticket keys match as whole words)
aside sessions              # your recent chats and their labels
```

Only chats with a jot in them can be found, so start a ticket's chat with
`>> session: SUP-4821 …`.

On macOS and Windows, `aside open 12` opens the chat a jot came from, for jots
made in Claude Desktop (checked on Desktop 2.19675.0 on a Mac and 2.19675.1 on
Windows 10). When a chat has such a link,
`aside find` prints the command under its resume command, as above. `aside show 12` prints the link as
`open:`, and the MCP tools return it as `open_url` with an `open_command`. Codex
app jots get a link too. On Windows it opened the Codex app on the right thread
(2026-10-08); if it doesn't, restart the Codex app and try again. It hasn't
been tried on a Mac. A jot with no link, such as one from a terminal `claude`
session, gets its resume command instead.

### Turn a jot into a GitHub issue

```sh
aside promote 12 --dry-run      # preview the repo, title and body; creates nothing
aside promote 12                # show the issue, ask [y/N], then create it with gh and mark the jot done
aside promote 12 --yes          # skip the question (needed when stdin is not a terminal)
aside promote 12 --with-reply   # include the agent's reply (Claude Code jots)
```

The issue goes to the repo the jot was captured in, or `--repo owner/name`. It
uses your existing `gh` login.

### Let your agent read your notes

```sh
claude mcp add --scope user aside -- /Users/you/go/bin/aside mcp   # Claude Code
codex mcp add aside -- /Users/you/go/bin/aside mcp                 # Codex
```

`$JOT_DB` sets the database path (`aside paths` shows where). Codex doesn't
pass your shell environment to MCP servers, so a custom `JOT_DB` has to go in
the `aside` MCP server entry's `env`, not only in the shell:

```toml
[mcp_servers.aside.env]
JOT_DB = "/path/to/jot.db"
```

Then ask the agent things like "what's in my inbox?", "where did I work on
SUP-4821?" or "which chats have I jotted in recently?". The tools are listed in
[docs/setup.md](docs/setup.md). The server is **read-only**: through it, agents can read and
search your jots, but not add, change or close them. An agent that can run
shell commands can still run `aside` itself: `aside done` when you ask it to
close jots, or `aside promote`, which posts a jot to GitHub. `promote` asks
first and refuses to run when stdin isn't a terminal, so an agent has to pass
`--yes`, which shows in the command (a pseudo-terminal can still answer it).
Your agent's command approval is what guards that, and in auto-approve modes
little or nothing does.

### Teach your agent the jot routines

The aside skill tells an agent how to use those tools: "update jot 12" checks
the jot's session for signs it was done and offers `aside done` with a note,
plus a follow-up jot for any open work left behind; "update all" does the same
across the inbox.

In Claude Code, in the terminal or in Claude Desktop, install it as a plugin:

```text
/plugin marketplace add kacxx/aside-jot
/plugin install aside@aside-jot
```

You get the skill from the last release, not from whatever is on `main`. Update
it with `claude plugin update aside@aside-jot`. It needs aside 0.4.0 or later
installed and `aside setup claude` run (that hook records the jots). The MCP
server above is recommended: reads then go through read-only tools instead of
shell commands, and their descriptions give the agent the full path to aside.
Without it, `aside` must be on your `PATH`.

If you copied the skill into `~/.claude/skills/aside` by hand, remove that copy
when you install the plugin, so the two don't drift apart.

Codex, and any other host the plugin doesn't reach, take a manual copy of the
skill from this repo:

```sh
git clone https://github.com/kacxx/aside-jot.git
mkdir -p ~/.agents/skills && cp -R aside-jot/skills/aside ~/.agents/skills/   # Codex
```

In PowerShell, use `Copy-Item -Recurse -Force aside-jot\skills\aside "$HOME\.agents\skills\"`
(create the folder first if it doesn't exist). Copy it again after updating
aside, so the skill matches the binary.

## All commands

| Command | What it does |
| --- | --- |
| `aside add <text>` | Save a jot from the terminal (reads stdin if no text) |
| `aside inbox [-n N] [--older D]` | Open jots, newest first (default 20, `-n 0` for all) |
| `aside show <id>` | One jot with its context |
| `aside search <query>` | Search all jots |
| `aside find <query>` | Chats with a matching jot, and how to resume them |
| `aside open <id>` | Open the chat a jot came from (macOS and Windows; Claude Desktop and Codex app jots) |
| `aside sessions [-n N]` | Recent chats with their labels (default 10) |
| `aside activity <id>` | What a jot's own chat did after it (Claude Code and Claude Desktop jots) |
| `aside done <id>... [--note "why"]` | Mark jots done; `--note` records why (search finds it) |
| `aside promote <id> [--repo owner/name] [--with-reply] [--yes] [--dry-run]` | Turn a jot into a GitHub issue; asks first unless `--yes` |
| `aside backup <path>` | Copy the database safely; never overwrites |
| `aside paths` | Where the data and binary are |
| `aside setup claude\|codex [--dry-run] [--mcp]` | Add the prompt hook to the agent's config file |
| `aside version` | Print the version |
| `aside hook claude\|codex\|cursor` | Hook entry point, used by the agent configs |
| `aside mcp` | Read-only MCP server, used by the agent configs |

## More detail

- [docs/setup.md](docs/setup.md): Windows, project-level configs, Codex trust,
  MCP for Codex's config file, and known Claude Code and Codex behaviour.
- [docs/reference.md](docs/reference.md): the capture rule, what happens when
  saving fails, how `find` matches, `promote` in full, and storage.
- [docs/cursor.md](docs/cursor.md): why Cursor isn't supported, and what was
  tested.
- [docs/decisions.md](docs/decisions.md): why aside works the way it does, and
  what's left out on purpose.
- [AGENTS.md](AGENTS.md): building, testing and contributing.
