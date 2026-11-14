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
  `find` reads every Claude Code transcript, so `show` and `brief` look up all
  their items' work items in one read: on a Mac with 2.3 GB of transcripts, a
  `brief` with 76 open items naming 54 keys took about 4 s, against about
  100 s with one `find` per key (checked 2026-11-12, on the change in #35).
- **`brief`** lists a session stamped on more than one open item once,
  instead of beside each item: a session that logs on behalf of others can be
  stamped on dozens. After the items, under `Sessions stamped on several open
  items`, its line is the one it would have had beside an item, with
  `, on N items` after the stamp (and label): `chat:<stamp>, on N items:
  <resume command>`. If the items use different lengths of stamp, the first
  one seen is shown. The items still show their stamps. The same stamp text on
  several items that matches no session is listed once the same way. A session
  found by a work item stays beside the items it isn't stamped on; on an item
  it's also stamped on, it counts as stamped, since a session that ran
  `worklog add` with a key is found by that key. `show` keeps every session
  beside its item. (Checked 2026-11-14 on the change in #47, on a throwaway
  log with a session stamped on three open items and one done item.)

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

The grammar is in [worklog-format.md](worklog-format.md): workstreams, item
lines, the facts after ` — `, and how an item is added and closed. It comes
from the worklog skill the log is kept with, whose status script reads the
same file, and `internal/worklog/ledger.go` follows it.

Two things are specific to this command:

- A `chat:` stamp it writes is the first 8 characters of the session ID. It
  reads longer ones too.
- A line that doesn't parse (a date that isn't one, a done item with no `done`
  date) is never rewritten. `brief` and `show` mark it, and `close` refuses it.

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
