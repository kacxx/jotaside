# AGENTS.md

Context for coding agents working on jotaside. Read this first. The README is
the short user guide; the detail of user-facing behaviour is in
[docs/reference.md](docs/reference.md) (capture, failure, `find`, `promote`,
storage), [docs/setup.md](docs/setup.md) (hooks and MCP per agent) and
[docs/cursor.md](docs/cursor.md). Why things are the way they are is in
[docs/decisions.md](docs/decisions.md); check it before reopening a settled
question.

## What jotaside is

A Go CLI that captures notes ("jots") typed as `>> …` in an AI coding agent.
An agent hook sees the prompt, saves it to a local SQLite database with its git
context, and blocks it so the model never sees it. Jots are read back with the
CLI or a read-only MCP server, and can be promoted to GitHub issues.

Supported: Claude Code and Codex. Cursor is not supported (see below). The
command has been renamed twice, `jot` → `aside` → `jotaside`; see the first
fact below before touching names.

## Code map

- `cmd/jotaside/` — CLI entry point, flag parsing and all terminal output
  (`printList`, `printSession`, `age`).
- `internal/hooks/` — one file per agent (`claude.go`, `codex.go`,
  `cursor.go`) decoding its payload and shaping its replies; `prompt.go` is
  the gating sequence they share.
- `internal/capture/` — the `>>` rule, deliberately narrow: `>>`, spaces
  or tabs (or non-breaking spaces), then a non-space character, after any leading `<system-reminder>` blocks.
- `internal/app/` — the service layer: capture, inbox, done, `promote.go`
  (jot to GitHub issue via `gh`), `sessions.go` (`find` and `sessions`).
- `internal/store/` — SQLite (pure Go, no cgo), one `entries` table, owner-only
  file permissions.
- `internal/setup/` — `jotaside setup`: finds the binary path, quotes it, and
  merges the hook into the agent's JSON config (order-preserving, with backup);
  `mcp.go` reads the agent's MCP config to spot a server under an old name.
- `internal/gitctx/` — best-effort git context with a time budget.
- `internal/mcp/` — the read-only MCP server (`inbox`, `show`, `search`, `find`, `sessions`, `activity`).
- `testdata/hooks/<agent>/` — recorded hook payloads used by tests and the
  smoke test.
- `test/e2e/run.sh` — black-box install and hook flow, run in CI.

## Commands

```sh
gofmt -l .                 # must print nothing
go vet ./...
go test -race ./...
golangci-lint run          # v2.14.0, config in .golangci.yml
bash test/e2e/run.sh       # end-to-end, throwaway database
bash test/release/smoke.sh  # install the latest release (TAG=vX.Y.Z for another) as a new user would
```

CI runs all of these on Linux. The release smoke test runs after each release
on macOS, Linux and Windows (`release-smoke.yml`). There it installs into the
default locations (`~/.claude`, `~/.codex`, the OS data folder), since the
runner is a new machine. Run locally, it redirects the configs and database
to a temp folder instead, so your real ones aren't touched, and works with
jotaside already installed. On Windows it runs `go vet` and `go test`
(without `-race`, which needs cgo).

`go install ./cmd/jotaside` installs to `$(go env GOPATH)/bin`, which is
`~/go/bin` unless `GOPATH` is set.

Use a throwaway database (`JOT_DB="$(mktemp -d)/jot.db"`) for any manual
run; never write test jots to the real one.
[docs/SMOKE_TEST.md](docs/SMOKE_TEST.md) has the manual checks, including
`find` and `sessions`, and [docs/mvp-validation.md](docs/mvp-validation.md)
records the results for the initial version.

## Facts that are easy to get wrong

- **The command was renamed `jot` → `aside` → `jotaside`.** `jot` was
  shadowed by `/usr/bin/jot` on macOS and the BSDs, so hooks ran the wrong
  program; `aside` collided with Aside, an AI web browser that also owns the
  Homebrew cask `aside`. Don't reintroduce a `jot` or `aside` binary or
  alias. The data directory (`…/jot/jot.db`) and the `JOT_*` environment
  variables keep the old name on purpose, so existing databases keep working
  without a migration. `jotaside setup` replaces hooks written under either
  old name (`previousNames` in `internal/setup/config.go`) and points out an
  MCP server still registered as `aside` or `jot`. Issues and PRs from before
  the rename are in kacxx/aside-jot (now private and archived).
- **Pass-through must be silent.** For an ordinary prompt, the Claude Code and
  Codex hooks print nothing: both add hook stdout to the model's context.
  Cursor's hook prints `{"continue":true}`.
- **A failed save still blocks.** If the database is unwritable, a `>>` prompt
  is blocked with "NOT saved" and the original text.
- **The Claude Code and Codex hooks exit 2 on a block.** Both skip a block
  whose JSON they reject and send the prompt to the model (Codex rejects even
  an unknown field); exit 2 blocks anyway, showing stderr, so the block
  message goes on stderr too. Claude Code uses valid JSON and ignores stderr;
  Codex shows stderr whenever the hook exits 2. The Cursor hook exits 0.
- **Cursor runs Claude Code hooks too.** It imports user hooks from
  `~/.claude/settings.json` and runs them on `beforeSubmitPrompt` next to its
  own, then merges the replies and joins their messages. So one Cursor prompt
  can reach jotaside twice; captures are deduplicated by Cursor's
  `generation_id` (`CaptureRequest.OnceKey`), and only the reply that saved
  the jot carries a message.
- **A repeated jot is saved once.** Claude Code runs the prompt hook only
  when it takes a queued prompt off the queue, so a jot sent while the agent
  is busy shows no `✓ Jotted` until the turn ends, and people re-send it.
  Hook captures with a session id skip a jot whose text (whitespace
  collapsed, case kept) the same session saved within `repeatWindow` (a
  minute) and hasn't closed, and reply `✓ Already jotted #N` (`store.InsertDedup`, #8).
- **Cursor sends blocked prompts anyway.** It keeps a blocked prompt in the
  chat and sends it with the next message. This is a Cursor bug
  (aside-jot #25) that jotaside can't work
  around, which is why Cursor is unsupported. Don't test jots in a Cursor chat
  you care about.
- **Resume directories differ by agent.** Claude Code stores a session under
  the directory it started in, which can differ from where a jot was captured
  if the agent changed directory. `sessions.go` reads the first `cwd` in the
  Claude transcript (`transcript_path` in the jot's metadata). Codex resumes
  from any directory, so its resume command has no `cd`.
- **Claude Desktop's session id is a hint.** Desktop sets
  `CLAUDE_CODE_HOST_SESSION_ID` (`local_<uuid>`) and `CLAUDE_CODE_ENTRYPOINT`
  on each session's Claude Code process; the hook stores them as
  `claude_desktop_session_id` and `claude_entrypoint` when well-formed. Both
  are undocumented, and a terminal `claude` started inside a Desktop session
  may inherit them, so check the id against Desktop's session file
  (`cliSessionId`) before building a link from it
  (aside-jot #44); `internal/app/links.go`
  does this and builds `open_url`; `jotaside open` opens it, because clicking
  the link in a Desktop chat doesn't work.
- **The MCP server is read-only by design.** Through it, agents can read jots
  but not create, edit or close them. That's all it guarantees: an agent with
  shell access can run `jotaside done` or `jotaside promote` (which publishes to
  GitHub), guarded only by its command approval, which may be automatic. Don't
  add write tools to the server, and don't claim more than this in docs.
- **Hook paths are absolute.** Hook and MCP configs use the full path to the
  binary; `jotaside paths` warns when a bare `jotaside` would run something else.

## Working agreements

- Changes go through a PR on a branch, with tests. CI must be green.
- Releases come from release-please: it reads conventional commit messages
  on `main` and keeps a release PR open. Merging that PR tags the release.
  Only `feat:`, `fix:` and breaking (`feat!:`) commits make a release; `docs:`,
  `ci:` and `chore:` commits appear in no release on their own. Commits
  without a prefix are ignored. The repo squashes with the commit message
  (single-commit PRs) or the PR title (several commits), so put the prefix on
  the commit message itself, and on the PR title too.
- After a feature merges, test it by hand: hook payloads into a throwaway
  database, then the real database read-only. Check printed commands against
  the files they point at.
- Ask before posting to GitHub (comments, issues, reviews) and before
  replacing the installed binary or editing hook configs. Back up anything
  replaced.
- When you post a PR review, post it as a formal review, not a plain comment:
  `gh api repos/OWNER/REPO/pulls/N/reviews -f event=COMMENT -f commit_id=SHA -f body=…`,
  with `commit_id` set to the commit you actually read. Left out, it defaults
  to the head when the review is posted.
- README examples must match real output. Docs claim only what was verified,
  and say which version it was verified on.
- If a ticket's work shows a decision it implements (on its `Implements:` line, or in its own `### D<n>` decisions) is wrong, impossible or contradicted, run the `flag-decision` skill (dev-flow plugin) and stop. If that skill isn't available, stop anyway and comment on the ticket saying which decision is wrong and why. Don't redesign inside the ticket.

## Current work

Open issues are the roadmap and the status: `gh issue list -R kacxx/jotaside`
(issues from before the rename are in kacxx/aside-jot, now private and archived).
Don't keep status in this file.
