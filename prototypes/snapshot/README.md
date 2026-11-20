# jotaside snapshot (prototype)

A static, read-only page of every jot: which session it came from and where
it was jotted. It's for user acceptance testing: seeing the jots, their
sessions, and the repos and folders behind them all at once, to work out what
a view of jotaside should show. It's a prototype, not part of jotaside: it isn't in a
release, has no tests, and is free to change or go.

```sh
python3 prototypes/snapshot/jotaside-snapshot.py --open
```

On Windows, use `python` instead of `python3`. It's written for Python 3.8 or later.
It has been run on Python 3.10 on Windows only, with jotaside v0.1.8. It needs
`jotaside` on `PATH`, or in `~/go/bin`, or named with `--jotaside`.
`scripts/check.sh` and CI don't cover it.

The page has counts at the top and four views of the same jots:

- **Inbox:** the open jots, grouped by age (today, 1 to 6 days, 7 to 29
  days, 30 days or older). Each shows its session, its repo and branch or its
  folder, and when it was last checked (`jotaside check`).
- **By session:** each session with its label, Claude Code title, start
  folder, counts, and the commands that resume it or open its chat. Jots with
  no session, such as those from `jotaside add`, get a card of their own.
- **By repo or folder:** jots grouped by repo, or by folder when they weren't
  made in a repo, with the branches, subfolders and sessions seen there.
- **Every jot:** a table of every row, with its metadata.

A filter box above the views keeps only the jots holding every word typed: in
their text, or their session's label or id, repo, branch, folder or source. It
applies in every view. A `,` or `|` between terms means any of them. "Hide done jots" leaves
only the open ones.

## Where the data comes from

The script doesn't open the jot database itself.

1. `jotaside backup` writes a consistent copy into a new temp folder.
2. The script reads every row of that copy.
3. It runs jotaside's read-only MCP server (`jotaside mcp`) against the same
   copy, with `JOT_DB`, for what jotaside works out beyond the rows: each
   session's label, start folder, resume and open commands, and the open
   jots' session titles. So the page agrees with `jotaside sessions` and
   `jotaside inbox` instead of working them out a second time.
4. It removes the temp folder.

The MCP server returns at most 200 sessions and 200 open jots per call. Past
that, the page says so at the top. The older sessions then get their label
and start folder from the script, by jotaside's rules from the jots alone: the
newest `session:` jot names the session, else its oldest jot, and the start
folder is the first folder a jot was made in, oldest first. For a Claude Code
session that can differ from the folder its transcript started in, which the
server would have read. Those sessions have no commands. The older open jots
have no session title. If the server fails, the page says so and falls back
the same way for every session.

| Option | What it does |
|---|---|
| `--open` | Opens the page in the default browser |
| `--out FILE` | Where to write it. Default: `~/.cache/jotaside/jotaside-snapshot.html` |
| `--db FILE` | The database to read. Default: jotaside's own choice (`JOT_DB`, else its data folder) |
| `--jotaside PATH` | The jotaside to run. Default: the one on `PATH`, else `~/go/bin/jotaside` |

## Trying it without real jots

`make-fixture.py` writes a throwaway database of 13 made-up jots. Their fake
Claude Code transcripts go beside it, so titles and start folders show up
too:

```sh
dir=$(mktemp -d)
python3 prototypes/snapshot/make-fixture.py "$dir"
python3 prototypes/snapshot/jotaside-snapshot.py --db "$dir/fixture.db" --out "$dir/page.html" --open
```

## The page is private

It holds the jots' text and the folders they were made in. The script writes
only that one file and makes no network calls. It creates a new file beside
the target with mode 0600 and moves it into place, so a failed run should
leave the old page whole and replace a symlink at the target rather than
follow it. On macOS and Linux, mode 0600 means you alone can read it; this
hasn't been checked there yet. On Windows the file takes its folder's
permissions. Keep the page on the machine: don't write it into a repo or a
synced folder.
