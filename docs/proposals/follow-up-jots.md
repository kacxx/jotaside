# Follow-up jots when closing a jot (#95): proposal

> Written before the tool was renamed `jotaside` (2026-10-24). Names here are
> the ones of the time (the `jot` or `aside` command, repo kacxx/aside-jot), and
> issue and PR numbers refer to [kacxx/aside-jot](https://github.com/kacxx/aside-jot).

Status: **decided**. Sliced into #97, #98, #99 and #102 (D9's CLI fix, split out of #98).
Source: #95

Decisions D1 to D9 come from the grill on #95 (2026-10-14), recorded in the issue body. D10 to D15 come from a second grill on the review comment on #95 (2026-10-14).

## Problem

When "update jot N" finds the jot done but leaves work behind, that work ends up only in the `done --note`. The jot then leaves the inbox, and nothing brings the follow-up back.

Example (2026-10-14): a jot asking for a staging test key was done, but the test the key was for was still blocked on access. The agent put the blocker in the suggested `--note` and told the user to type a new `>>` jot if they wanted a reminder. It can't create jots itself, by design.

## Decisions

### D1: When to offer a follow-up

The agent offers a follow-up only when the records show open work that nothing else tracks (no Jira ticket, PR or other open jot covers it). A follow-up that duplicates a ticket is just more inbox noise.

### D2: Partly done jots

When the work is partly done, the agent offers both keeping the jot open and closing it with a follow-up, and the user picks.

### D3: Follow-up wording

The text reads `follow-up to #<id>: <the open work>`, written by the agent in its own words. Naming the source jot keeps the link to the session through `aside show <id>`. Own words keep it short and stop transcript text from being copied into a command.

### D4: Safe command form

The offered command passes the text through stdin with a quoted heredoc, not as an argument:

```
/full/path/to/aside add <<'EOF'
follow-up to #29: <the open work>
EOF
```

Inside `<<'EOF'` nothing expands, so running the command can't execute anything hidden in the text.

### D5: `aside add --from <id>`

Not tracked for now. Naming the jot (D3) is enough until there's evidence the missing session link matters.

### D6: Command order and note

The `aside add` line comes before the `aside done` line, and the done note ends in "; follow-up added as a new jot". Adding first means the follow-up exists before the original leaves the inbox.

### D7: "Update all"

Each follow-up line sits under its jot's done line and doesn't count toward the 10-session limit. The limit is about reading transcripts, and a follow-up costs no extra reads.

### D8: Run from the original repo

The offered command first `cd`s to the source jot's repo root (from `aside show <id>`), so the follow-up is listed under the same repo. It is left out if that folder no longer exists. Otherwise the follow-up records whatever folder the user runs it from.

### D9: Windows command form

On Windows, the agent offers a single-quoted here-string piped into aside instead of a heredoc:

```powershell
@'
follow-up to #29: <the open work>
'@ | & 'C:\path\to\aside.exe' add
```

A single-quoted here-string expands nothing, as with D4. This issue also includes the CLI fix that strips a leading BOM from `aside add`'s stdin, so Windows follow-ups don't start with a stray character.

### D10: Run `done` only if `add` succeeded

The offered command makes `aside done` depend on `aside add` succeeding: on macOS and Linux, they're chained with `&&` on the heredoc's first line, and on Windows, the `done` line is wrapped in `if ($?) { … }`. `$?` is used, not `$LASTEXITCODE`, because `$LASTEXITCODE` is not updated when `aside.exe` can't be started, so a stale 0 would let `done` run (confirmed on Windows PowerShell 5.1.19041, 2026-10-15). This stops the original closing with "follow-up added as a new jot" when no follow-up was added.

### D11: A "partly done" verdict

A fourth verdict, "partly done", sits next to done, no sign yet and can't tell, in both "Updating a jot" and "Updating all jots". It's clearer than a sub-case, and step 5 offers D2's two choices for it.

### D12: No status checks for follow-up jots

"Update" doesn't read the original jot's records for a jot that starts with "follow-up to #N:", for now. The skill says follow-up jots are left out of "Updating all jots" because they have no session. This is the cost D5 already accepted, and nobody has missed it yet.

### D13: How the agent checks D1

Before offering a follow-up, the agent searches the inbox for the open work and checks the records for a ticket or PR that covers it. One sentence in step 5 gives D1 a concrete check.

### D14: No subshell

The command keeps the plain `cd` and `Set-Location`. Ending up in the repo folder is harmless, and a subshell around a heredoc makes the command harder to read.

### D15: The command form still follows aside's path

The rule based on aside's path stays, and Git Bash is listed as a known risk in the proposal. Detecting the user's actual shell adds complexity for an edge case.

## Shape

**Skill (`skills/aside/SKILL.md`):**
- "Updating a jot", step 3: add the fourth verdict, "partly done" (D11).
- "Updating a jot", step 5: before offering a follow-up, search the inbox for the open work and check the records for a ticket or PR that covers it (D13). When the records show open work and nothing else tracks it (D1), offer the follow-up command before the `aside done` line (D6). The follow-up text is `follow-up to #<id>: <the open work>` in the agent's words (D3), and the done note ends in "; follow-up added as a new jot" (D6).
- For a "partly done" verdict, offer two options: keep it open, or close it with the follow-up (D2, D11).
- "Updating all jots": the "partly done" verdict is used here too (D11). Each follow-up line goes under its jot's done line and doesn't count toward the 10 sessions (D7). Say that follow-up jots are left out because they have no session (D12).
- "Things to leave to the user" is unchanged: the agent never runs `aside add` itself.

**Command form.** The agent picks the form from the full path to aside it already uses: a path ending in `.exe` or starting with a drive letter gets the Windows form (D15).

macOS and Linux (D4, D8, D10, D14):

```
cd '<repo root>' && /full/path/to/aside add <<'EOF' && /full/path/to/aside done 29 --note "<evidence>; follow-up added as a new jot"
follow-up to #29: <the open work>
EOF
```

If the `cd` or the `add` fails, `done` doesn't run (probed in zsh and bash).

Windows (D9, D8, D10, D14):

```powershell
Set-Location -LiteralPath '<repo root>'
@'
follow-up to #29: <the open work>
'@ | & 'C:\path\to\aside.exe' add
if ($?) { & 'C:\path\to\aside.exe' done 29 --note "<evidence>; follow-up added as a new jot" }
```

Defaults the decisions leave to the implementer:
- The repo root is the `root:` line of `aside show <id>` on the CLI, or the `repo_root` field of the MCP `show` tool, which the skill reads first. If the jot has no repo root, or the folder no longer exists, the `cd` / `Set-Location` line is left out, and on macOS and Linux the chain starts at `aside add` (D8).
- A repo root containing a single quote is also left out, rather than escaped.
- On Windows the follow-up text is plain ASCII (no curly quotes or em dashes). What Windows PowerShell 5.1 sends to a native program depends on the user's `$OutputEncoding`: the default is ASCII, which turns other characters into `?`. ASCII text arrives intact under the default or UTF-8. A piped here-string was seen arriving with a leading BOM under UTF-8 and under what was believed to be the default (see the fact-check), which the CLI change below strips.

**CLI (`cmd/aside/main.go`, `add`):** when the text comes from stdin, strip one leading U+FEFF before `Capture` (D9). Tests in `cmd/aside/main_test.go`: stdin `"\ufeffbom jot\r\n"` is stored as `bom jot`, and stdin that is only a BOM is treated as empty, like empty stdin. Text given as arguments is unchanged. This change doesn't depend on the skill changes and can ship first.

## Fact-check

| Claim | Result | Source |
|---|---|---|
| `aside add` records the folder it is run from, with its repo root, name and branch | ✅ | `cmd/aside/main.go:165-166`, `internal/app/app.go:87-96`; probe: `aside add` from go-lib stored `cwd`, `repo`, `root`, `branch` |
| `aside add` has no flag to copy another jot's session or metadata | ✅ | `cmd/aside/main.go:156-170` (only text, `Source: "cli"`, `Cwd`) |
| `aside add` reads the text from stdin when given no arguments | ✅ | `cmd/aside/main.go:157-164`; probe with a heredoc |
| Capture trims surrounding whitespace, so a heredoc's trailing newline or `\r\n` isn't stored | ✅ | `internal/app/app.go:83`; probe: `"…jot\r\n"` stored as `…jot` |
| `sessions` leaves out jots with no session | ✅ | `internal/app/sessions.go:211` |
| `find` lists jots with no session under "Not in a session" (CLI) and `not_in_a_session` (MCP) | ✅. This corrects the issue's fact list, which said `find` leaves them out | `internal/app/sessions.go:126`, `internal/mcp/server.go:449-461`; probe: `aside find follow-up` printed "Not in a session: #1" |
| `done --note` text is searchable | ✅ | `internal/store/store.go:376-383` (`json_extract(metadata, '$.done_note') LIKE ?`) |
| `aside show <id>` prints the jot's session and repo root | ✅ | probe: `show` printed `session:` and `root:` lines |
| The skill already says never to run `aside add` and to offer the command instead | ✅ | `skills/aside/SKILL.md:95-98` |
| The skill's "Updating a jot" step 5 offers `aside done`, and "Updating all jots" has a 10-session limit | ✅ | `skills/aside/SKILL.md:56`, `:65` onward |
| A quoted heredoc (`<<'EOF'`) expands nothing | ✅ | probe in zsh and bash: `$(echo PWNED)`, `` `id` `` and `"quotes"` were stored literally and nothing ran |
| A single-quoted PowerShell here-string expands nothing | ✅ (docs; not probed, no PowerShell on the test machine) | [about_Quoting_Rules](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_quoting_rules) (5.1 and 7): "In single-quoted here-strings, variables aren't replaced by their values" |
| A leading BOM on `aside add`'s stdin is stored today | ✅ | probe: stdin `EF BB BF "bom jot"` stored with the BOM |
| A BOM is stripped only when reading config files | ✅ | `internal/setup/config.go:43`; no other BOM handling in `*.go` |
| `"text" \| aside add` in Windows PowerShell 5.1 stored U+FEFF | ✅ | owner, Windows results on #77 ("BOM from a PowerShell pipe is stored") |
| #80 left the PowerShell-pipe BOM for later | ✅ | #80 description ("Left for later: the BOM from a PowerShell pipe into `aside add`") |
| Windows PowerShell 5.1 pipes to native programs in `$OutputEncoding`, which defaults to ASCII | ✅ | [about_Preference_Variables (5.1)](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_preference_variables?view=powershell-5.1#outputencoding): "Default: ASCIIEncoding object" |
| A here-string piped into `aside.exe add` in Windows PowerShell 5.1 arrives with a leading BOM under the default ASCII `$OutputEncoding`, not only under UTF-8 | ⚠️ the BOM was seen with the setting believed to be the default, but the session's actual value wasn't recorded, and a PowerShell profile can change it; Microsoft's docs say ASCII has no BOM to send. To confirm: in `powershell -NoProfile`, check `$OutputEncoding.WebName` is `us-ascii`, then pipe a here-string into `aside.exe add` and look for U+FEFF. Nothing depends on it: `aside add` strips the BOM either way | owner, [Windows run on #106](https://github.com/kacxx/aside-jot/pull/106#issuecomment-6183806372) |
| In bash and zsh, `cd … && aside add <<'EOF' && aside done …` skips `done` when the `cd` or the `add` fails, and still expands nothing in the heredoc | ✅ | probe in zsh and bash with stand-ins: a missing folder and a failing command each skipped the last command; `$(echo PWNED)` and `` `id` `` passed through literally |
| `aside add` with empty stdin fails with "empty jot" | ✅ | `internal/app/app.go` (`CaptureOnce`: `errors.New("empty jot")`); probe: exit status 1 |
| The MCP `show` tool returns the jot's `repo_root` | ✅ | `internal/mcp/server.go:428-440` returns the `app.Entry`; `internal/store/store.go:83` (`json:"repo_root,omitempty"`) |

## Out of scope

- `aside add --from <id>` (D5).
- Any MCP server change: the agent offers commands; it gets no write tool.
- Changing how the existing `aside done --note "<evidence>"` line is quoted (see Risks).
- A hook payload that starts with a BOM, reported on #77 as passing through uncaptured. Hosts don't normally send one.

## Risks

- **The existing `--note` is double-quoted.** The skill already offers `aside done <id> --note "<the evidence>"`, and the evidence is drawn from the transcript, so a `$(…)` in it would run when the user runs the command. D4 protects the new `add` line; the `done` line has the same exposure today. Worth its own issue.
- **Terminator lines.** A follow-up line that is exactly `EOF` would end the heredoc early, and on Windows a line starting with `'@` (or a curly `’@`, since PowerShell treats curly quotes as quotes) would end the here-string. A one-line follow-up in the agent's words makes this unlikely.
- **Non-ASCII on Windows.** What reaches aside depends on the user's `$OutputEncoding`: under Windows PowerShell 5.1's default ASCII, any non-ASCII character becomes `?`. Shape keeps the Windows text ASCII, which arrives intact under the default or UTF-8 (a UTF-16 setting sends two bytes per character). PowerShell 7 defaults to UTF-8 and is unaffected.
- **Git Bash on Windows (D15).** The agent picks the form from aside's path, so a Git Bash user gets the PowerShell form and has to adapt it.
- **`Set-Location` failing on Windows.** The `$?` check (D10) covers `aside add`, not `Set-Location`. If the folder can't be entered, the follow-up is still added, from whatever folder the user is in.
- **"Tracked elsewhere" is the agent's call (D1).** It can miss a ticket and offer a duplicate follow-up. The user approves every `add`, so the cost is one extra jot.
