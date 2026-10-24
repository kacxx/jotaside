# jotaside

Jot it aside: a side channel for thoughts while you work with coding agents.

Mid-task, you think of something unrelated: a bug to check, a ticket to raise,
a question for someone. Telling the agent derails it, and switching to a notes
app breaks your flow. With jotaside, you type it into the agent with `>>` in
front:

```
>> token cache TTL looks too long, check with infra before shipping
✓ Jotted #12
```

jotaside saves it with its git context (repo, branch, commit). In **Claude Code**
and **Codex** it also blocks the prompt, so the model never sees it and the
work in front of you keeps going. That is the don't-disrupt-my-flow feature.
It needs the hook to run: in Codex, it doesn't until you trust it in `/hooks`
([setup](docs/setup.md#codex-hook-by-hand)). And a jot sent while the agent is
busy can be queued and merged with other prompts, and then reach the model
([capture rules](docs/reference.md#capture-rules)).

**Cursor isn't supported.** Cursor keeps a blocked prompt in the chat and sends
it to the model with your next message, so a `>>` jot there still reaches the
model ([why](docs/cursor.md)). In Cursor, jot from a terminal with `jotaside add`
instead.

![Demo: a ">>" prompt is blocked and saved with its git context, Claude makes the change, "update jot 1" checks the session and closes the jot with a note, and the inbox still holds the other jot](docs/demo.gif)

*Recorded with the real Claude Code CLI (v2.1.309) and v0.7.0, before the
rename, so it shows the old command name, `aside`; the commands are otherwise
the same. [`docs/demo/record.sh`](docs/demo/record.sh) re-records it.*

## Install

With Go 1.25+:

```sh
go install github.com/kacxx/jotaside/cmd/jotaside@latest
jotaside setup claude    # or: jotaside setup codex
```

Without Go, download the archive for your system from the
[latest release](https://github.com/kacxx/jotaside/releases/latest)
(macOS, Linux and Windows, amd64 and arm64). The files are
`jotaside_*_darwin_arm64.tar.gz`, `jotaside_*_darwin_amd64.tar.gz`,
`jotaside_*_linux_arm64.tar.gz`, `jotaside_*_linux_amd64.tar.gz`,
`jotaside_*_windows_arm64.zip` and `jotaside_*_windows_amd64.zip`. Put `jotaside`
somewhere it will stay, such as `~/.local/bin`, then run setup from there,
because setup records the path it was run from. On Apple Silicon:

```sh
shasum -a 256 -c checksums.txt --ignore-missing    # if you downloaded checksums.txt too
tar -xzf jotaside_*_darwin_arm64.tar.gz jotaside
mkdir -p ~/.local/bin && mv jotaside ~/.local/bin/
~/.local/bin/jotaside setup claude    # or: setup codex
```

On Linux the archive name changes and the rest is the same. On arm64 (use
`jotaside_*_linux_amd64.tar.gz` on x86_64):

```sh
sha256sum -c checksums.txt --ignore-missing    # if you downloaded checksums.txt too
tar -xzf jotaside_*_linux_arm64.tar.gz jotaside
mkdir -p ~/.local/bin && mv jotaside ~/.local/bin/
~/.local/bin/jotaside setup claude    # or: setup codex
```

`~/.local/bin` isn't on `PATH` in a new shell. Add
`export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc` on macOS, or to
`~/.bashrc` on Linux, and open a new shell to run plain `jotaside`.

The binaries aren't signed. On macOS, a file downloaded in a browser is marked
as quarantined, extracting it keeps the mark, and Gatekeeper may refuse to run
it; clear it with `xattr -d com.apple.quarantine ~/.local/bin/jotaside`. Downloads
with `curl` or `gh release download` aren't marked. On Windows, unzip
`jotaside_*_windows_amd64.zip` (or `jotaside_*_windows_arm64.zip`) and put
`jotaside.exe` in a folder where it will stay, then run
`.\jotaside.exe setup claude` from there in PowerShell.

`jotaside setup` adds the `UserPromptSubmit` hook to `~/.claude/settings.json`
(or `~/.codex/hooks.json`) with the binary's absolute path, since editors often
run hooks with a minimal `PATH`. It keeps your other settings and hooks, and
replaces an older jotaside entry instead of adding a second, including one
from before the rename (`aside hook claude` or `jot hook claude`). When that file
already exists, setup copies it to `<file>.bak-<time>` first. When it does not
exist yet, the backup line says `none needed, the file did not exist`.
`--dry-run` shows the result and writes nothing; `--mcp` also prints the
command that gives the agent read-only access to your jots (it doesn't run
it). If `jotaside` isn't on your `PATH` yet, run it by its full path:
`"$(go env GOPATH)/bin/jotaside" setup claude` after `go install`, or
`~/.local/bin/jotaside setup claude` for a downloaded binary.

A first install, where the config file is new, prints the checkmark and then
a note that Cursor imports the same file:

```
$ jotaside setup claude
✓ Added the jotaside hook in /Users/you/.claude/settings.json
  command: /Users/you/.local/bin/jotaside hook claude
  backup:  none needed, the file did not exist
  Other settings and key order are kept; only whitespace may differ.

Note: Cursor imports hooks from ~/.claude/settings.json, so this also turns jotaside on in
Cursor, which isn't supported (see https://github.com/kacxx/jotaside/blob/main/docs/cursor.md).
Use --dry-run to preview without writing.

Next: type `>> test` in Claude Code: you should see ✓ Jotted #N and no model reply.
```

For **Codex**, run `/hooks` in the **Codex CLI** afterwards and **trust** the
hook. Until it's trusted, `>>` prompts go to the model.

Prefer to edit the JSON yourself? See [docs/setup.md](docs/setup.md).

### Versions and updating

`@latest` installs the newest release; pin one with a tag such as `@v0.1.0`. What changed in
each release is in [CHANGELOG.md](CHANGELOG.md), and `jotaside version` prints the
one you have:

```
$ jotaside version
jotaside v0.1.0
```

To update, run the same `go install …@latest` again, or `mv` the new
downloaded binary over the old one at the same path (no need to rerun setup).
Use `mv` rather than `cp`: on Apple Silicon, copying over the old file can get
the new one killed on start. A browser download needs the `xattr` step again.
Hooks pick up the new binary straight away. The MCP server keeps running the old one until its
agent restarts, so restart Claude Desktop, Codex and any open agent sessions.

### Coming from aside

jotaside was called `aside` (and before that `jot`), from the repo
[kacxx/aside-jot](https://github.com/kacxx/aside-jot); its v0.7.0 is the last
`aside` release. Your jots stay where they are: the database folder is still
named `jot` and the `JOT_*` variables are unchanged. To switch:

1. Install jotaside as above, then run `jotaside setup claude` (or `codex`).
   It replaces the `aside hook` (or `jot hook`) entry with one `jotaside hook`
   entry; in Codex, trust the changed hook in `/hooks`.
2. If you added the MCP server as `aside`, setup prints the commands that
   replace it with `jotaside`. Restart the agent afterwards.
3. If you installed the skill plugin, swap it:
   `claude plugin uninstall aside@aside-jot`,
   `claude plugin marketplace remove aside-jot`, then install `jotaside` as
   in [Teach your agent the jot routines](#teach-your-agent-the-jot-routines).
   Replace a copied `skills/aside` folder with `skills/jotaside`.
4. Delete the old `aside` binary (`aside paths` shows where it is).

### Check it works

Type `>> test` in the agent. In Claude Code you should see
`UserPromptSubmit operation blocked by hook` above `✓ Jotted #N`, and no reply
from the model. That blocked line is Claude reporting that the hook stopped
the prompt; `✓ Jotted` means the jot was saved. Then run `jotaside inbox` in a
terminal. If your shell can't find `jotaside`, add `$(go env GOPATH)/bin` (after
`go install`) or the folder you put the downloaded binary in to your `PATH`.

Windows paths, project-level configs, Codex trust details and known agent
behaviour: [docs/setup.md](docs/setup.md).

### What jotaside keeps

The hook sees every prompt you send, but keeps only jots: prompts that start
with `>>`, a space and some text (the [capture rule](docs/reference.md#capture-rules)
has the details). Anything else passes through untouched and isn't stored.

For each jot, jotaside saves the text, the time, the agent and session it came
from, the folder, the git repo, branch and commit, and the session's transcript
path when the agent gives one. It all goes into one SQLite file on your machine
(`jotaside paths` shows where), readable only by you on macOS and Linux.

Some commands also read, on your machine, the transcript of the session a jot
came from: `activity` (and "update jot N") to see what the session did next,
`sessions` and `find` for the folder a chat started in, and `promote
--with-reply` for the agent's reply. `open` and `find` read Claude Desktop's
session list to link jots back to their chats.

jotaside makes no network connections. The one thing that sends anything anywhere
is `jotaside promote`, which posts to GitHub through your `gh` login, after asking.

### Uninstall

1. Remove the jotaside entry from the `UserPromptSubmit` hooks in
   `~/.claude/settings.json` (or `~/.codex/hooks.json`). `jotaside setup` backed
   the file up first (`<file>.bak-<time>`), but that copy is missing any
   changes you've made since.
2. Remove the MCP server, if you added it: `claude mcp remove --scope user jotaside`
   or `codex mcp remove jotaside`.
3. Remove the skill: `claude plugin uninstall jotaside@jotaside` (and
   `claude plugin marketplace remove jotaside`), or delete a copied
   `skills/jotaside` folder.
4. Delete the binary (`jotaside paths` shows where it is).
5. To delete your jots too, delete the database folder `jotaside paths` shows,
   after a `jotaside backup` if you might want them back.

## A day with jotaside

- **Morning.** Deep in a refactor, you jot three things without stopping:
  `>> SUP-4821 needs a backend ticket` (#21), `>> ask infra about the cache
  TTL` (#22) and `>> flaky test in billing, look later` (#23).
- **Lunch.** `jotaside inbox` lists them, newest first, with the repo and branch
  each came from. You fix the flaky test and run `jotaside done 23`.
- **Afternoon.** The ticket note deserves an issue. Since you jotted it in
  Claude Code, `jotaside promote 21 --with-reply` shows you an issue with your jot
  and what the agent had just said when you jotted it, and creates it in that
  repo when you say yes. (`promote` always asks first.)
- **Friday.** Someone asks about SUP-4821. `jotaside find SUP-4821` lists the
  chats where you jotted about it, with the command to reopen each one.

## Using jotaside

### Capture

| You type | What happens |
| --- | --- |
| `>> some thought` (in Claude Code or Codex) | Saved with the repo, branch, commit and chat; the model never sees it |
| `>> session: SUP-4821 token TTL` | Also labels the chat, so `jotaside find` and `jotaside sessions` show why it exists |
| `jotaside add some thought` (in a terminal) | Saved with the git context of the current directory |

Only a prompt that **starts** with `>>`, a space and then text is a jot (extra
spaces are fine). `>>x`, ` >> x` and `a >> b` go to the model as normal. To
send a prompt that starts with `>> `, put a space or a backslash in front.

In Claude Code or Codex, if a jot shows up as a chat message instead of
`✓ Jotted`, resend it once the agent is idle.

### Review

```sh
jotaside inbox                 # your open jots, newest first, with their age
jotaside inbox --older 7       # only the ones that have waited a week or more
jotaside show 12               # one jot with its full context
jotaside search ttl            # search every jot, done ones included
jotaside done 12               # mark it done; it drops out of the inbox
jotaside done 3 4 5 --note "pushed to Q4, see https://example.com/wiki/plan"   # several at once, with why
```

```
$ jotaside inbox
#14   today  SUP-4821 needs a backend ticket  [api@main] · SUP-4821 review
#12   2d     token cache TTL looks too long, check with infra before shipping  [api@main]
#11   2d     session: SUP-4821 token TTL investigation  [api@main]
```

When available, a Claude Code session's name follows the location (` · SUP-4821
review` above); jotaside reads it from the transcript when listing the inbox.

### Get back to a chat

Every jot made in an agent remembers its chat. `jotaside find` shows the chats
where you jotted about something, and prints the command to reopen each one:

```
$ jotaside find SUP-4821
SUP-4821 token TTL investigation
  claude · api@main · 2 jots · last 2d (#12)
  #11   2d     session: SUP-4821 token TTL investigation
  cd /Users/you/code/api && claude --resume 0f3c9a…
  jotaside open 12

Not in a session:
#14   2026-10-04 18:03  SUP-4821 needs a backend ticket  [api@main]
```

```sh
jotaside find SUP-4821         # chats that mention it (ticket keys match as whole words)
jotaside sessions              # your recent chats and their labels
```

Only chats with a jot in them can be found, so start a ticket's chat with
`>> session: SUP-4821 …`.

On macOS and Windows, `jotaside open 12` opens the chat a jot came from, for jots
made in Claude Desktop (checked on Desktop 2.19675.0 on a Mac and 2.19675.1 on
Windows 10). When a chat has such a link,
`jotaside find` prints the command under its resume command, as above. `jotaside show 12` prints the link as
`open:`, and the MCP tools return it as `open_url` with an `open_command`. Codex
app jots get a link too. On Windows it opened the Codex app on the right thread
(2026-10-08); if it doesn't, restart the Codex app and try again. It hasn't
been tried on a Mac. A jot with no link, such as one from a terminal `claude`
session, gets its resume command instead.

### Turn a jot into a GitHub issue

```sh
jotaside promote 12 --dry-run      # preview the repo, title and body; creates nothing
jotaside promote 12                # show the issue, ask [y/N], then create it with gh and mark the jot done
jotaside promote 12 --yes          # skip the question (needed when stdin is not a terminal)
jotaside promote 12 --yes --public # the same, for a public repo (--yes alone refuses one)
jotaside promote 12 --with-reply   # include the agent's reply (Claude Code jots)
```

The issue goes to the repo the jot was captured in, or `--repo owner/name`. It
uses your existing `gh` login. If that repo is public, or `gh` can't say,
the preview warns you first, and `--yes` alone is refused.

### Let your agent read your notes

```sh
claude mcp add --scope user jotaside -- /Users/you/go/bin/jotaside mcp   # Claude Code
codex mcp add jotaside -- /Users/you/go/bin/jotaside mcp                 # Codex
```

`$JOT_DB` sets the database path (`jotaside paths` shows where). Codex doesn't
pass your shell environment to MCP servers, so a custom `JOT_DB` has to go in
the `jotaside` MCP server entry's `env`, not only in the shell:

```toml
[mcp_servers.jotaside.env]
JOT_DB = "/path/to/jot.db"
```

Then ask the agent things like "what's in my inbox?", "where did I work on
SUP-4821?" or "which chats have I jotted in recently?". The tools are listed in
[docs/setup.md](docs/setup.md). The server is **read-only**: through it, agents can read and
search your jots, but not add, change or close them. An agent that can run
shell commands can still run `jotaside` itself: `jotaside done` when you ask it to
close jots, or `jotaside promote`, which posts a jot to GitHub. `promote` asks
first and refuses to run when stdin isn't a terminal, so an agent has to pass
`--yes`, which shows in the command (a pseudo-terminal can still answer it),
and `--yes` alone is refused for a public repo.
Your agent's command approval is what guards that, and in auto-approve modes
little or nothing does.

### Teach your agent the jot routines

The jotaside skill tells an agent how to use those tools: "update jot 12" checks
the jot's session for signs it was done and offers `jotaside done` with a note,
plus a follow-up jot for any open work left behind; "update all" does the same
across the inbox.

In Claude Code, in the terminal or in Claude Desktop, install it as a plugin:

```text
/plugin marketplace add kacxx/jotaside
/plugin install jotaside@jotaside
```

You get the skill from the last release, not from whatever is on `main`. Update
it with `claude plugin update jotaside@jotaside`. It needs jotaside
installed and `jotaside setup claude` run (that hook records the jots). The MCP
server above is recommended: reads then go through read-only tools instead of
shell commands, and their descriptions give the agent the full path to jotaside.
Without it, `jotaside` must be on your `PATH`.

If you copied the skill into `~/.claude/skills/jotaside` by hand, remove that copy
when you install the plugin, so the two don't drift apart.

Codex, and any other host the plugin doesn't reach, take a manual copy of the
skill from this repo:

```sh
git clone https://github.com/kacxx/jotaside.git
mkdir -p ~/.agents/skills && cp -R jotaside/skills/jotaside ~/.agents/skills/   # Codex
```

In PowerShell, use `Copy-Item -Recurse -Force jotaside\skills\jotaside "$HOME\.agents\skills\"`
(create the folder first if it doesn't exist). Copy it again after updating
jotaside, so the skill matches the binary.

## All commands

| Command | What it does |
| --- | --- |
| `jotaside add <text>` | Save a jot from the terminal (reads stdin if no text) |
| `jotaside inbox [-n N] [--older D]` | Open jots, newest first (default 20, `-n 0` for all) |
| `jotaside show <id>` | One jot with its context |
| `jotaside search <query>` | Search all jots |
| `jotaside find <query>` | Chats with a matching jot, and how to resume them |
| `jotaside open <id>` | Open the chat a jot came from (macOS and Windows; Claude Desktop and Codex app jots) |
| `jotaside sessions [-n N]` | Recent chats with their labels (default 10) |
| `jotaside activity <id>` | What a jot's own chat did after it (Claude Code and Claude Desktop jots) |
| `jotaside done <id>... [--note "why"]` | Mark jots done; `--note` records why (search finds it) |
| `jotaside promote <id> [--repo owner/name] [--with-reply] [--yes [--public]] [--dry-run]` | Turn a jot into a GitHub issue; asks first unless `--yes`, which needs `--public` for a public repo |
| `jotaside backup <path>` | Copy the database safely; never overwrites |
| `jotaside paths` | Where the data and binary are |
| `jotaside setup claude\|codex [--dry-run] [--mcp]` | Add the prompt hook to the agent's config file |
| `jotaside version` | Print the version |
| `jotaside hook claude\|codex\|cursor` | Hook entry point, used by the agent configs |
| `jotaside mcp` | Read-only MCP server, used by the agent configs |

## More detail

- [docs/setup.md](docs/setup.md): Windows, project-level configs, Codex trust,
  MCP for Codex's config file, and known Claude Code and Codex behaviour.
- [docs/reference.md](docs/reference.md): the capture rule, what happens when
  saving fails, how `find` matches, `promote` in full, and storage.
- [docs/cursor.md](docs/cursor.md): why Cursor isn't supported, and what was
  tested.
- [docs/decisions.md](docs/decisions.md): why jotaside works the way it does, and
  what's left out on purpose.
- [AGENTS.md](AGENTS.md): building, testing and contributing.
