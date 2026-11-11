# worklog

`worklog` is a second command in this repo, separate from `jotaside`. It
keeps a markdown worklog's items linked to the agent sessions that worked on
them: it adds and closes items, stamps each with the calling session's ID,
and prints a command that resumes each session. It reuses jotaside's
transcript, resume-command and `find` code.

It isn't in a release yet. Install it from a checkout:

```sh
go install ./cmd/worklog
```

## Commands

```sh
worklog add <workstream> <text>   # add an open item and commit it
worklog close <item or key>       # mark the one matching open item done and commit it
worklog show <item or key>        # the matching items, each with its sessions
worklog brief                     # every open item with its sessions
```

- **`add`** names the workstream by its `id`, or by its title or part of
  either. The text is the description, optionally followed by ` — ` and
  facts: `worklog add outage-followup "Write the retro — due 2026-09-02 · ABC-1290"`.
  It writes the owner: `me`, or the `@person` you gave, with `since <today>`
  if they have no date. The item goes after the workstream's last item.
- **`close`** matches open items by text, or by a ticket key as a whole word,
  and refuses when more than one matches. It writes `done <today>`, drops
  `me` and the due date, and keeps a person, keys and stamps.
- **Both** add the calling session's stamp as one more fact,
  `chat:<first 8 characters of its ID>`, so run them from the session doing
  the work. With no session ID they still write, and say the item has no stamp.
- **`show` and `brief`** find a session from the stamp on the item, then from
  jotaside's `find` for each work item the line names. A Claude Code session
  is printed with its resume command. A stamp with no Claude Code transcript
  is looked up among jotaside's sessions, which is where a Cursor chat is.

## When it won't write

`add` and `close` change nothing when:

- the log isn't in a git repo, or that repo has a remote (the log holds
  private notes and stays local);
- the log has uncommitted changes;
- the line to close doesn't parse (a bad date); it is reported instead;
- the commit fails, in which case the log is put back as it was.

## Environment

| Variable | Meaning |
|---|---|
| `WORKLOG_LEDGER` | The log. Default `~/worklog/ledger.md` |
| `CLAUDE_CODE_SESSION_ID` | The calling session's ID, set by Claude Code |
| `CURSOR_CONVERSATION_ID` | The same in Cursor, used when the first isn't set |
| `CLAUDE_CONFIG_DIR` | Claude Code's folder, for transcripts. Default `~/.claude` |
| `JOT_DB` | jotaside's database |

## The log's grammar

The grammar comes from the worklog skill the log is kept with, which has a
script that reads it. This is the part `worklog` reads and writes; the
skill's own format document is the source.

```markdown
# Worklog — <owner>
last-reconciled: 2026-08-31

## Service outage follow-up
id: outage-followup · opened: 2026-08-24 · links: ABC-1234
Action items from last week's outage.

- [ ] Guard on runtime processing — @alex since 2026-08-31 · ABC-1290
- [ ] Incident retro written up — me · due 2026-09-02
- [x] Assign the guard ticket — done 2026-08-31 · chat:a1b2c3d4
```

- **Workstream:** a `## <title>` heading. The line starting `id: ` under it
  holds `·`-separated `key: value` pairs; `id` is the workstream's stable
  name, since the title may change.
- **Item:** `- [ ]` (open) or `- [x]` (done), a description, then ` — ` and
  `·`-separated facts, all optional. The facts follow the line's *last*
  spaced em-dash, so a description may have its own.

| Fact | Meaning |
|---|---|
| `me` | The owner's own action (the default when no owner is written) |
| `@person` | Delegated: the item is waiting on them |
| `since <ISO date>` | When it was delegated, usually written `@person since <date>` |
| `due <ISO date>` | When the owner's action is due |
| `done <ISO date>` | When it was closed. Required on a `- [x]` item |
| a ticket key or URL | A link; older logs put several, space-separated, in one fact |
| `chat:<id>` | A session that worked on the item: the first 8 characters of its ID, or more |

A line that doesn't parse (a date that isn't one, a done item with no `done`
date) is never rewritten.

## Checked, and not

Checked on Windows on 2026-11-12, on the branch that added the command:

- A log written by `add` and `close` (an owned item with a due date and a
  key, a delegated one, one with an em-dash in its description, each then
  closed) was read by the skill's status script with no unreadable lines,
  and with the owner, dates and links it should have.
- On five of the script's own test logs, `brief` listed the same open items
  as the script, and marked the one open line the script rejects.
- `show` printed a resume command whose directory and ID matched the calling
  Claude Code session's transcript.

Not checked: that Cursor sets `CURSOR_CONVERSATION_ID` in an agent's shell.
The code reads it; nobody has run `worklog` from a Cursor chat yet.
