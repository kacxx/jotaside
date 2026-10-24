# Security

## Reporting a problem

Please report security problems privately, not in a public issue: use
**Report a vulnerability** on the repository's
[Security tab](https://github.com/kacxx/jotaside/security). You'll get a reply
there.

Fixes go into the latest release only, so check the problem is still there in
[the latest release](https://github.com/kacxx/jotaside/releases/latest).

## What counts

jotaside sees every prompt you type into a hooked agent, so the things worth
reporting are ones that break what it promises:

- A prompt that doesn't start with `>> ` being blocked, saved or changed.
- A jot reaching the model when the host is one jotaside supports.
- The MCP server adding, changing or closing jots, or reading outside the
  database and the transcripts recorded with jots.
- `jotaside promote` posting without asking, or a jot's text or transcript text
  getting run as a command.
- The database or its backups being readable by other users on Unix.

jotaside makes no network connections of its own: `jotaside promote` posts through
your `gh` login.
