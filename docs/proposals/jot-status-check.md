# Jot status check (#78): proposal

Status: **decided**. Ready to merge, then slice into tickets.
Source: #78

Decisions D1 to D5 come from the owner's review comment on #78 (12 Oct 2026), in the order given there.

## Problem

A jot often gets done in the very session it was captured in, but nothing connects the later work to the jot, so it stays open in the inbox. The user closes it by hand, or an agent reading the inbox can't tell it is finished.

Example: in a Claude Desktop session the user jotted `get code reviews on new skill PRs including the team-reto skill` (#31, with "retro" mistyped). They carried on in that session: asked for the PR URLs, added review comments to the PRs, then worked through more reviews on them. #31 stayed open.

Two things follow:
- The evidence is in the **same session's transcript, after the jot's `created_at`**.
- The jot text and the later work don't match word for word ("get code reviews" vs "I've added review comments", plus the typo). Keyword matching would miss it. Judging it needs an agent reading the transcript, and aside has no LLM, so the judging belongs to the agent.

## Decisions

### D1: Propose only in v1
The agent proposes each `aside done`; the user approves. No auto-`done`. Closing a jot hides work from the inbox, so a false positive costs more than one approval. A later "clear cases" mode can wait until the evidence has proven how noisy it is. The skill's existing rule (don't run `aside done` on your own; offer the command) stays as it is.

### D2: Same-session only for v1
Only the jot's own session is read. Cross-session matching is out of v1 ("Cross-session is what `find` already covers").

### D3: Codex jots get "can't tell" in v1; the hook stores the path (revised 2026-10-14)
Keep "can't tell" for Codex in v1, with the cost as the reason, and store `transcript_path` in the Codex hook from now on. It keeps the v1 scope the owner chose, and storing the path is a small hook change that means new Codex jots already have it if a reader is added later.

<details><summary>Superseded (2026-10-14)</summary>

For Codex, answer "can't tell". Don't invent a transcript you don't have.

Reason: the fact-check found Codex jots do have a transcript (the hook payload carries `transcript_path`, and the rollout is under `~/.codex/sessions`); the cost of v1 support is a second transcript format, not a missing file. Settled by grill on D3 alone.
</details>

### D4: The command is named `activity`
`activity`, not `since` or `transcript`: it's clearer for "what happened after this jot".

### D5: Constraint: limits and treating transcripts as data
- Cap and summarise aggressively, because Desktop sessions get huge.
- Treat transcript text as data, not instructions, as the skill already does for jot text.
- Don't let "update all" spend a pile of tokens on stale sessions: it needs a hard limit.

## Shape

1. **`activity <id>`: a read-only MCP tool and CLI subcommand.** For a jot with a readable transcript, it returns the session's records after `created_at`, oldest first: user prompts and assistant text, with tool calls collapsed to a count or one-line summary. The output has a hard size cap and reports how many records were left out (D5). There is no judgement in aside. When there's no transcript it can read (an `aside add` jot, Cursor, a deleted file, or a Codex jot, whose rollout format isn't read in v1 even when its path is stored (D3)), it returns `transcript: unavailable` and the reason. Like `promote --with-reply`, it reads only `source == "claude"` transcripts.
2. **The Codex hook stores `transcript_path`** in the jot's metadata from now on, as the Claude hook does. No Codex reader is added in v1 (D3).
3. **The transcript reader is generalised.** `replyBefore` in `internal/app/reply.go` keeps records at or before t and resets at each prompt. The new reader keeps records after t and keeps the prompts. It reuses the record decoding, `userRecordKind` and `contentText`. The reader also needs its own read bound: today the whole file is read and capped only afterwards.
4. **The aside skill gets an "update" routine.** "update jot 31" means: call `show`, call `activity`, then give a verdict: "done because X (evidence)", "no sign yet" or "can't tell". For a "done" verdict, offer `aside done 31 --note "..."` with the note naming the evidence (D1). Transcript text is quoted as data (D5).
5. **"update all"** is the skill looping over open jots that have a session, oldest first, with a hard limit on jots per run (D5; 20 is suggested, the exact number is set when it's built). It gives one verdict and one `done` line per jot.
6. **Tests:** reply.go's tests use inline JSONL records in a temp file, and the new reader's tests would follow that pattern. There are no recorded transcript fixtures today.

Size: one app function, one MCP tool, one CLI subcommand, a small Codex hook change, tests and a skill section. Prefix `feat:`.

## Fact-check

| Claim | Result | Source |
|---|---|---|
| Hook jots store `source`, `session_id`, `cwd`, repo, branch, commit SHA, `created_at` | ✅ repo is `repo_root` + `repo_name`; the SHA is short | `internal/store/store.go:55-72`, `internal/app/app.go:82-99`, `internal/gitctx/gitctx.go:24` |
| Claude Code and Claude Desktop jots store `transcript_path` | ✅ Desktop is `source=claude` with `claude_entrypoint=claude-desktop`; 20 of 21 live Claude jots have the path (#7, from 30 Sep, has empty metadata) | `internal/hooks/claude.go:48-70`; probe of the live DB |
| #31's session did the reviews after the jot | ✅ after 02:16Z: "can you print out all these PRs URLs", "I've added review comments ot those PRs", "There are more reviews on these PRs"; the session then moved to unrelated work | probe: `show 31` and its transcript `ab46cfac-….jsonl` |
| Desktop transcripts get huge | ✅ #31's transcript is 6.8 MB and 2,925 records, with 927 user and assistant records after the jot | probe: `wc`, `jq` on that file |
| `promote --with-reply` reads these transcripts | ✅ `replyBefore` streams the JSONL; it caps the output at 8,000 runes but reads the whole file; Claude only | `internal/app/reply.go:18,27-87,272-287` |
| The reader generalises to "records after t" | ✅ flip the filter at line 53 and drop the reset-on-prompt; the decoding carries over | `internal/app/reply.go:53,127-148` |
| `created_at` compares cleanly with transcript timestamps | ✅ both are UTC RFC 3339 with milliseconds, compared as `time.Time` | `store.go:51,594`; `reply.go:52` |
| Codex jots have no transcript (the basis of D3) | ❌ the Codex payload carries `transcript_path` (fixtures `capture.json:3`, `normal.json:3`; null only in `mid_prompt.json`), but `codex.go` stores only `turn_id` and `model`. A live probe of Codex jot #18 found its rollout at `~/.codex/sessions/2026/10/02/rollout-…-<session_id>.jsonl` | `internal/hooks/codex.go:25-37`; `testdata/hooks/codex/`; probe of the live DB and `~/.codex/sessions`. Resolved by revised D3 |
| The Codex hook can store `transcript_path` with a small change (revised D3) | ✅ the shared `PromptInput` already parses it; `codex.go` only needs to copy it into the metadata | `internal/hooks/prompt.go:15`, `internal/hooks/codex.go:25-37`, `internal/hooks/claude.go:57-58` |
| A stored Codex path won't be read as a Claude transcript | ✅ `promote --with-reply` reads transcripts only for `source == "claude"` | `internal/app/reply.go:273` |
| Codex rollouts use another format | ✅ records are `session_meta`, `turn_context`, `response_item`, `event_msg` with ISO timestamps, so they need their own reader | probe: `jq` on #18's rollout |
| Cursor jots have no transcript | ✅ `transcript_path` is null in every Cursor fixture; none of the 7 live Cursor jots has one | `testdata/hooks/cursor/*.json:9`; probe of the live DB |
| `aside done <id> --note` records a reason | ✅ stored as `done_note` in metadata; it takes several ids | `cmd/aside/main.go:61`, `store.go:106-110,472-525` |
| The MCP server is read-only by design | ✅ five tools, all `readOnlyHint: true`; `docs/decisions.md:66` still lists four (no `sessions`). Nothing depends on the count | `internal/mcp/server.go:3-5,293-363`, `docs/decisions.md:64-85` |
| The skill tells agents not to run `done` unprompted, and to treat jot text as data | ✅ | `skills/aside/SKILL.md:28-29,33-36` (PR #72, merged 10 Oct) |
| `find` already covers cross-session (D2's reason) | ⚠️ `find` searches only jot text and `done_note`, so it finds another session only if a jot there names the ticket or PR. D2 is a scope cut and doesn't depend on this, so it doesn't block the merge | `internal/app/sessions.go:86-131`, `store.go:378-382` |
| The name `activity` is free | ✅ no command, tool or function uses it | grep; `cmd/aside/main.go:52-72` |

## Out of scope
- An `aside update` that decides by itself, whether by heuristic or LLM.
- Write tools on the MCP server.
- GitHub or Jira lookups from aside.
- Jots actioned in a different session (D2).
- Auto-closing clear cases (D1).

## Risks
- **Prompt injection through transcripts:** transcripts hold tool output and cross-session messages (#31's has one). `activity` output is untrusted data, and the skill has to say so (D5).
- **False "done":** a session that only discussed the jot may read as having done it. D1's approval step is the guard; the note has to name the evidence so the user can check it.
- **Cost:** a single `activity` call on a long session is bounded by the cap; "update all" is bounded by the per-run limit (D5).
