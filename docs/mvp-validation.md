# MVP validation

> Written before the tool was renamed `jotaside` (2026-10-24). Names here are
> the ones of the time (the `jot` or `aside` command, repo kacxx/aside-jot), and
> issue and PR numbers refer to kacxx/aside-jot (now private and archived).

Record of the checks run on the initial import (commit `01a73eb`) before the
real-use trial. Environment: Linux 6.18, 4 cores, Go 1.25.0, git 2.43.0.

## Hook contract vs. current docs

Checked against the Claude Code hooks reference (`UserPromptSubmit`) and the
Cursor hooks docs (`beforeSubmitPrompt`) on 2026-09-27. No discrepancies:

- **Claude Code.** `{"decision":"block","reason":…}` blocks the prompt and
  erases it from context. `reason` is shown to the user and not added to
  context. `suppressOriginalPrompt: true` hides the original text in the block
  message, which is why the failure message repeats it. Plain stdout on exit 0
  *is* added to Claude's context, so pass-through must print nothing.
- **Cursor.** Input has `conversation_id`, `generation_id`, `workspace_roots`
  and `prompt`. Output is `{"continue":bool,"user_message":…}`.

## Unit tests

`go test -race -count=1 -v ./...`

```
--- PASS: TestCLIFlow (0.09s)
--- PASS: TestHookAlwaysExitsZero (0.00s)
PASS
ok  	github.com/kacxx/aside-jot/cmd/jot	1.109s
--- PASS: TestDBPath (0.00s)
--- PASS: TestBusyTimeout (0.00s)
--- PASS: TestServiceFlow (0.02s)
PASS
ok  	github.com/kacxx/aside-jot/internal/app	1.037s
--- PASS: TestMatch (0.00s)
PASS
ok  	github.com/kacxx/aside-jot/internal/capture	1.020s
--- PASS: TestBranch (0.02s)
--- PASS: TestDetachedHEAD (0.02s)
--- PASS: TestNoCommits (0.01s)
--- PASS: TestNonRepo (0.00s)
--- PASS: TestMissingDirAndEmpty (0.00s)
PASS
ok  	github.com/kacxx/aside-jot/internal/gitctx	1.072s
--- PASS: TestClaudePassThrough (0.00s)
--- PASS: TestClaudeCaptureInRepo (0.07s)
--- PASS: TestClaudeCaptureDetachedHEAD (0.05s)
--- PASS: TestClaudeCaptureNonGitDir (0.02s)
--- PASS: TestClaudeDBOpenFailure (0.00s)
--- PASS: TestClaudeDBLocked (0.12s)
--- PASS: TestCursorPassThrough (0.00s)
--- PASS: TestCursorSingleRoot (0.04s)
--- PASS: TestCursorMultiRoot (0.05s)
--- PASS: TestCursorFailureBlocks (0.00s)
PASS
ok  	github.com/kacxx/aside-jot/internal/hooks	1.366s
--- PASS: TestSession (0.04s)
--- PASS: TestUnknownProtocolVersionFallsBack (0.00s)
PASS
ok  	github.com/kacxx/aside-jot/internal/mcp	1.053s
--- PASS: TestCRUD (0.03s)
--- PASS: TestBadMetadataRejected (0.01s)
--- PASS: TestSearchEscapesWildcards (0.03s)
--- PASS: TestWALEnabled (0.01s)
--- PASS: TestConcurrentWriterPools (0.66s)
--- PASS: TestBackup (0.03s)
PASS
ok  	github.com/kacxx/aside-jot/internal/store	1.796s
```

## End-to-end (built binary, fixtures piped through `jot hook`)

Throwaway `$JOT_DB`. Capture fixtures had `cwd` / `workspace_roots` rewritten
to this checkout, or to a non-git temp dir, with `jq`. These runs were made in
a clone of `kacxx/Claude_Code_Scratch_Pad`, where the code was first pushed by
mistake before moving here, so that is the repo and branch the captures record.

```
== claude: pass-through (expect no stdout) ==
normal      stdout=[] exit=0
mid_prompt  stdout=[] exit=0
malformed   stdout=[] exit=0
== claude: capture in this repo ==
{"decision":"block","reason":"✓ Jotted #1","suppressOriginalPrompt":true}
exit=0
== claude: capture, non-git dir ==
{"decision":"block","reason":"✓ Jotted #2","suppressOriginalPrompt":true}
exit=0
== cursor: pass-through ==
normal      {"continue":true} exit=0
malformed   {"continue":true} exit=0
== cursor: single root ==
{"continue":false,"user_message":"✓ Jotted #3"}
exit=0
== cursor: multi root ==
{"continue":false,"user_message":"✓ Jotted #4"}
exit=0
== DB open failure (fails safe, exit 0) ==
{"decision":"block","reason":"✗ Jot NOT saved (create data dir: mkdir /dev/null: not a directory). Your text:\n\n>> token cache TTL looks too long, check with infra before shipping","suppressOriginalPrompt":true}
exit=0
{"continue":false,"user_message":"✗ Jot NOT saved (create data dir: mkdir /dev/null: not a directory). Your text:\n\n>> orders pagination should probably be cursor-based, not offset"}
exit=0
== inbox ==
#4    2026-09-27 06:57  shared-lib version bump needs a changelog entry
#3    2026-09-27 06:57  orders pagination should probably be cursor-based, not offset  [Claude_Code_Scratch_Pad@claude/aside-jot-mvp-b3ufow]
#2    2026-09-27 06:57  token cache TTL looks too long, check with infra before shipping
#1    2026-09-27 06:57  token cache TTL looks too long, check with infra before shipping  [Claude_Code_Scratch_Pad@claude/aside-jot-mvp-b3ufow]
== show 1 ==
#1  (inbox)

token cache TTL looks too long, check with infra before shipping

created:   2026-09-27T06:57:05Z
source:    claude
session:   3f1c2a9e-7b4d-4e0a-9c55-0d2f8e6b1a47
cwd:       /home/user/Claude_Code_Scratch_Pad
repo:      Claude_Code_Scratch_Pad
root:      /home/user/Claude_Code_Scratch_Pad
branch:    claude/aside-jot-mvp-b3ufow
commit:    01a73eb
metadata:  {"transcript_path":"/Users/dev/.claude/projects/-Users-dev-src-webapp/3f1c2a9e-7b4d-4e0a-9c55-0d2f8e6b1a47.jsonl"}
== show 4 ==
#4  (inbox)

shared-lib version bump needs a changelog entry

created:   2026-09-27T06:57:05Z
source:    cursor
session:   71c3e8b2-9f0a-4d6e-b5a7-2e8c1f4d0b93
metadata:  {"generation_id":"d62f0b3a-8e17-4c9d-a054-1b7e3c9f2a68","workspace_roots":["/Users/dev/src/webapp","/Users/dev/src/shared-lib"]}
== mcp ==
{"id":1,"r":"2025-06-18"}
{"id":2,"r":["inbox","show","search"]}
{"id":3,"r":"shared-lib version bump needs a changelog entry"}
== backup ==
✓ Backed up to $TMP/b.db
jot: backup: $TMP/b.db already exists; refusing to overwrite
exit=1
```

## Latency

Measured by spawning the binary with the fixture on stdin, from Python
`subprocess.run`, so the numbers include process spawn.

| Path | Runs | Median | p95 | Max |
| --- | --- | --- | --- | --- |
| `hook claude`, ordinary prompt | 300 | 3.13 ms | 3.91 ms | 9.54 ms |
| `hook cursor`, ordinary prompt | 300 | 3.28 ms | 4.16 ms | 6.27 ms |
| `hook claude`, capture (git + SQLite insert) | 100 | 10.85 ms | 12.86 ms | 15.14 ms |
| `/bin/true` (baseline) | 300 | 1.02 ms | 1.22 ms | 3.02 ms |

On the ordinary-prompt path, jot adds about 2 ms over a bare process start.
`strace -f -e trace=execve,openat` on that path shows no `git` exec and no
database file opened. With an unwritable `$JOT_DB`, an ordinary prompt still
produces no output and creates nothing.

## Found after import

A later `-race` run of `TestConcurrentWriterPools` failed 2 times in 30. When
several processes open a database file that doesn't exist yet, switching it to
WAL can return `SQLITE_BUSY` immediately, without calling the busy handler. The
failure was safe: the hook still blocked the jot and reported "NOT saved". It
could only happen the first time the database file was created. Fixed after the
import: `Open` retries, giving each attempt only what is left of the busy
timeout so the total wait stays within it, and the schema is created under
`BEGIN IMMEDIATE`. `TestOpenHonoursBusyTimeout` covers the bound. `TestConcurrentFirstOpen` covers it and failed 4 of 5
runs without the fix.
