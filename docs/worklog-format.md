# Worklog format

The grammar the `worklog` command (#42) reads and writes. It's the grammar an existing worklog skill already uses, so the two can write one log (kacxx/notebook#38, D10). Copied from that skill's grammar on 2026-11-11. Checked on #42's branch on 2026-11-12: a log written by its `add` and `close` was read by the skill's status script with no unreadable lines.

One `##` heading per live workstream, a metadata line, an optional description, then action items as checkboxes. Ticket keys in the example are placeholders; the grammar doesn't care about their shape.

```markdown
# Worklog — <owner>
last-reconciled: 2026-08-31

## Service outage follow-up
id: outage-followup · opened: 2026-08-24 · links: ABC-1234
Action items from last week's outage.

- [ ] Guard on runtime processing — @alex since 2026-08-31 · ABC-1290
- [ ] Incident retro written up — me · due 2026-09-02
- [x] Assign the guard ticket — done 2026-08-31 · chat:a1b2c3d4

## Customer escalation: rule not firing
id: esc-6784 · opened: 2026-08-27 · links: ESC-6784 · nudge: 2d
- [ ] Chase the customer for reproduction steps — me · due 2026-09-01
- [x] Followed up with the account team — done 2026-08-31
```

## Grammar

**Log header.** A `# ` title line, then `last-reconciled: <ISO date>` (or `none`).

**Workstream heading.** `## <title>`. The title is free text and may change; the `id` below is the stable name.

**Workstream metadata line.** The line after the heading: `·`-separated `key: value` pairs.

| Key | Required | Meaning |
|---|---|---|
| `id` | yes | Slug, stable. The name to use when adding to a workstream. |
| `opened` | yes | ISO date |
| `links` | no | Ticket keys, PR or wiki URLs, space-separated |
| `review` | no | Review cadence, default `1w` |
| `nudge` | no | Staleness threshold for delegated items, default `3d`; a deployment may set a tighter default for workstreams that link an escalation |

**Item line.** `- [ ]` or `- [x]`, a free-text description, then ` — ` and `·`-separated facts, all optional. The facts follow the line's *last* spaced em-dash, so a description may have its own. The owner defaults to `me` when absent.

| Fact | Meaning |
|---|---|
| `me` | The owner's own action (the default) |
| `@person` | Delegated: this is what makes the item *waiting on* |
| `since <ISO date>` | When it was delegated. Ages against `nudge` |
| `due <ISO date>` | When the owner's action is due |
| `done <ISO date>` | Closure date. Required on `- [x]` items |
| `<TICKET-KEY>` / URL | Link to a ticket or PR. Existing logs also put several space-separated keys in one fact |
| `chat:<id>` | Session where the work happened: the first 8 characters of its session ID |

Examples of facts in order: `— me · due 2026-09-02 · ABC-1290`, `— @alex since 2026-08-31 · ABC-1290`, `— done 2026-08-31 · ABC-1290 · chat:a1b2c3d4`.

**State** is derived, not declared:
- **open:** `- [ ]` with no `@person`;
- **waiting:** `- [ ]` with `@person`;
- **done:** `- [x]`, with a `done` date.

There is no blocked state: a blocker is a person to wait on, or a sentence in the description. Abandoned items are deleted, not tracked.

## Writing an item

- **Add:** `- [ ] <text> — <facts>`, after the workstream's last item. Facts follow ` — `; a stamp is one more fact, ` · chat:<first 8>`.
- **Close:** change `- [ ]` to `- [x]` and add `done <today>` (required on done items). Done items in existing logs carry no `me` or `due`, and delegated ones read `@person since … · done …`. Keep keys and `chat:` stamps.
- **Never reformat** a line that doesn't parse. Report it instead.

## Archiving

Archived blocks move verbatim to `archive/YYYY-QN.md` for the current quarter: a workstream whose items are all done moves whole.

The log lives in a local git repo with **no remote**: it holds private notes.
