# Routed jots (#30): design proposal

> Written before the tool was renamed `jotaside` (2026-10-24). Names here are
> the ones of the time (the `jot` or `aside` command, repo kacxx/aside-jot), and
> issue and PR numbers refer to [kacxx/aside-jot](https://github.com/kacxx/aside-jot).

Status: design approved on PR #54 (6 Oct 2026). Build waits until late October.

Built from issue #30, its 4 Oct review comment and docs/decisions.md.
Where this proposal follows that comment, it says so. Revised after the
review on PR #54.

## Summary

A jot that starts with a registered route (`jira: …`, `slack: …`) is tagged
with that route at capture. A worker lists open items for its route and closes
them with `aside done <id> --note <link>`. aside never calls Jira or Slack, has
no claiming, and has no LLM. Confirmation of outward actions is the user's
control, and aside can't enforce it (decision 5).

## Decisions

**1. Routes live in a config file next to the database** (`routes` file, one
route name per line). Not an environment variable: the hook runs in whatever
environment the editor gives it, the same problem that ruled out a tag allowlist
in "Ticket keys are matched at query time". `aside paths` prints where the file
is. Names are lower-case words (`[a-z][a-z0-9_-]*`); a line that doesn't fit,
and the reserved names `any` and `none` (decision 4), are ignored with a
warning from `aside paths`, not from the hook. Blank lines and `#` comments are
skipped. If the file is missing, nothing is routed and everything behaves as
today. This settles where the list lives (issue #30's open question 2) and the
format is plain names; question 2 below covers a richer format later.

**2. The route is fixed at capture.** It is stored in the existing `metadata`
JSON (`route`), in normal form (lower case, so `Jira:` stores `jira`), so there
is no schema change and no migration. Registering `todo` next month doesn't
queue old `todo:` jots, and removing a route doesn't orphan queued ones.

Matching: the text must start with `name:` and a space, then more text, and
`name` (case-insensitive) must be in the file. Anything else is ordinary text:
`todo:`, a typo, a bare `jira:`, and `jira:SUP-1` with no space after the colon,
which people will type. The stored text is unchanged (the `jira:` prefix stays).

This knowingly goes against "Ticket keys are matched at query time, not
tagged": a route fixed at capture has the same silent-miss failure. If the file
is missing when a jot is captured, the jot never routes, and there is no write
command to fix it later. The fix-at-capture choice is still right for the
`todo` reason above, so the misses are made visible instead:
- The hook reply shows the route: `✓ Jotted #41 → jira`. A `jira:` jot without
  the arrow is noticed straight away.
- The file is read only after `capture.Match` succeeds, so ordinary prompts
  don't pay for it. If it exists but can't be read, the jot is saved unrouted,
  the hook still exits 0, and the reply says `(routes file unreadable)` after
  the id, so the miss is seen at the time.

**3. Provenance is a signal, not a security boundary.** An agent with a shell
can forge either source: `aside add "jira: …"` (or `env -u CLAUDECODE aside
add …`), or `echo '{…}' | aside hook claude`. So:
- Hook captures from `claude` and `codex` route. `cursor` doesn't: Cursor is
  parked ([cursor.md](../cursor.md)) and nothing new is built for it.
- `aside add` does not route. This stops an agent that runs `aside add
  "jira: …"` innocently. It doesn't stop a deliberate one, which can pipe
  into the hook.
- Confirmation (decision 5) is the only control either way. The docs say the
  source check is not a boundary. Question 1 below records the answer.

**4. Worker API stays minimal.**
- `aside inbox --route jira` filters the inbox to that route. `--route any`
  means every routed jot and `--route none` means unrouted ones. `inbox`
  already lists only open jots, and the filter keeps that. Same filter on the
  MCP `inbox` tool, which stays read-only.
- `aside show` prints a `route:` line, and the MCP `inbox`, `show`, `search`
  and `find` results carry a `route` field. CLI list lines (`inbox`, `search`,
  and the loose jots in `find`) don't change: the text already begins with
  `jira:`.
- Closing is the existing `aside done <id>... [--note "why"]` (#43). A worker
  passes the result link as `--note <link>`. No new write command. The note
  must be passed with the flag: `aside done 1 <link>` fails, as `<link>` is
  read as an id.
- No claiming, lease or timeout. Add one only if an item is processed twice.
- No `abandoned` state. `done` with no note already means it (per the review).
  Add one only if abandoned items need reporting.

**5. Confirmation is the worker's contract, not code in aside.** The docs
state: a worker may only draft; the user confirms every Slack post, Jira ticket
or other outward action, every time. Jot text often holds pasted tickets or
logs, so a worker with write access acting unconfirmed is the prompt-injection
path. aside can't enforce this, so the docs won't claim it does.

**6. Routed jots still show in `aside inbox`**, unchanged:
`#41   today  jira: SUP-4821 needs a backend ticket  [aside-jot@main]`. They
stay in the user's own view, and `--route none` shows what isn't routed. No
new bracket marker: the text already starts with the route, and the trailing
`[…]` already means the location. The cost is deliberate: a jot typed with
`aside add`, or captured while the file was missing, prints like a routed one
but no worker will see it. `aside inbox --route none` is how to find those.

**7. `gh` route.** `aside promote` stays a command the user runs, and it asks
`[y/N]` unless `--yes` is passed (#45). `--yes` exists for scripts and is
required when stdin isn't a terminal, so an agent with a shell can pass it. The
`gh` route is therefore an exception to "the user confirms every outward
action", and the docs say so rather than claim `promote` always asks. Whether
`--yes` should go away once `gh` is a route is left out of this design: it
changes #45's decision, so it needs its own entry.

## Out of scope

Jira and Slack workers (they live outside the repo, as examples), aside calling
any service, and claiming.

## Rough size

One PR: route parsing in `internal/capture` (pure function, table tests like
`Match`), reading the `routes` file in `internal/app` after a match, the route
in the hook reply, `--route` on `inbox` plus the MCP tool, a `route` field in
`show`, `search`, `find` and MCP, `aside paths` printing the file, docs and a
decisions.md entry. Hook gating, `done` and the schema are untouched.

## Questions and answers

1. **Should `aside add` route?** No, for this design. `add` stays unchanged and
   only the hook path parses routes. If terminal routing is needed later it is
   its own change, most likely an explicit `aside add --route NAME` flag, so an
   agent writing `jira:` text by accident still doesn't route. Record it in
   decisions.md as deferred, not rejected. (Answered on PR #54.)
2. **Richer route file format?** Not now. Plain names, as in decision 1. A form
   like `jira = SUP-` can be its own change if a worker needs it.
3. **Timing.** The design is approved. The build waits until late October, as
   the #30 comment says. Until then, jots meant for Jira or Slack start with
   `jira:` or `slack:` and are stored as plain text. In late October,
   `aside search "jira:"` and `aside search "slack:"` show whether there is
   enough real use to build it and which worker to write first. (Answered on
   PR #54.)
