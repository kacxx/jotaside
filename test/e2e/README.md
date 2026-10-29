# End-to-end harness

A black-box check of the documented install/use flow against a freshly built
`jotaside` binary. It complements the unit tests (`go test ./...`) and the manual
[docs/SMOKE_TEST.md](../../docs/SMOKE_TEST.md) by exercising the real entry
points end to end:

1. `install` — builds the binary (from this checkout, or `@latest` outside it);
   pure-Go SQLite, no cgo.
2. `paths` / PATH — prints data + binary locations and whether `jotaside` is on `PATH`.
3. empty inbox on first run.
4. CLI round-trip — `add` / `inbox` / `search` / `show` / `done`.
5. hooks — a `>>` prompt is captured and a normal prompt passes through, for
   `claude`, `codex` and `cursor`, printing each agent's exact response schema.
   Pass-through must be exact: nothing at all for Claude Code and Codex (they
   add hook output to the model's context), `{"continue":true}` for Cursor.
   With an unwritable database, a `>>` prompt is still blocked and reported
   "NOT saved" with the original text. The Claude Code and Codex hooks exit 2
   and repeat the message on stderr; the Cursor hook exits 0.
6. MCP — a JSON-RPC `initialize` handshake.

On a developer's machine the binary under test is installed into a folder the
script creates under `${XDG_CACHE_HOME:-~/.cache}/jotaside-e2e` and removed at
the end, so the `jotaside` in `$(go env GOPATH)/bin` that your hooks and MCP
server use is left as it is. The folder is outside the system temp folder
because `jotaside setup` refuses to register a binary there. If it can't be
created, the script stops. In CI (`CI=true`) and in the clean-room container
(`JOTASIDE_E2E_EXPECT_NOT_ON_PATH=1`) there is nothing to protect, so it
installs into `$(go env GOPATH)/bin`, as the README tells a new user.

The database is redirected to a throwaway path (`JOT_DB` under `mktemp -d`), so
running it **never touches your real inbox**.

## Run it directly

```sh
bash test/e2e/run.sh
```

Run from the repo root, it installs from the checkout, so it tests the code you
have. This is what CI runs.

## Run it in a clean room

For a first-time-user reproduction (empty `HOME`, `@latest`, unprivileged user):

```sh
docker build -t jotaside-e2e -f test/e2e/Dockerfile .
docker run --rm jotaside-e2e
```

Behind a TLS-intercepting corporate proxy, see the CA note in
[`Dockerfile`](Dockerfile).
