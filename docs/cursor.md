# Cursor (not supported)

> **jotaside does not work with Cursor, and Cursor support is paused.** The hook
> saves the jot and blocks that turn, but Cursor keeps the blocked prompt in the
> chat and sends it to the model with your next message there
> (aside-jot #25). This is a
> [known Cursor bug](https://forum.cursor.com/t/prompt-blocked-by-a-beforesubmitprompt-hook-is-still-sent-to-the-model-with-the-next-message/173565),
> and jotaside can't work around it: the hook only decides whether a prompt is
> sent now. Don't rely on `>>` in Cursor; jot from a terminal with `jotaside add`
> instead. Cursor also runs the Claude Code hook from
> `~/.claude/settings.json`, so the same applies if only that one is installed.
>
> Cursor has reproduced the bug and is tracking it, with no date for a fix.
> Cursor recommends clicking the blocked message, editing it into the prompt you
> want to send, and resending. On Cursor 3.23.12 this kept the jot out of the
> model's context: after editing a blocked jot (#19) into a question, the model
> said it was the first message, and on the next turn quoted only the two
> messages sent after it. Starting a new chat also avoids the leak, since the
> jot is sent with the next message in the same chat.
>
> `jotaside hook cursor` stays in the binary, and the setup and test notes below
> are kept for when Cursor fixes the bug.

Add to `~/.cursor/hooks.json` (or `<project>/.cursor/hooks.json`):

```json
{
  "version": 1,
  "hooks": {
    "beforeSubmitPrompt": [
      { "command": "/Users/you/go/bin/jotaside hook cursor" }
    ]
  }
}
```

Cursor reloads this file on save.

Cursor also loads Claude Code user hooks from `~/.claude/settings.json` and runs
them on `beforeSubmitPrompt` next to its own, sending them
`hook_event_name: "beforeSubmitPrompt"`. On 3.23.12, with both hooks installed
and the Claude hook printing nothing, the prompt reached the model even though
`jotaside hook cursor` replied `{"continue":false}`. With the Claude hook removed,
the jot was blocked (aside-jot #23). So on
that event `jotaside hook claude` now replies exactly as `jotaside hook cursor` does:
it blocks jots and answers `{"continue":true}` to every other prompt. Either
hook is enough in Cursor, and both together are safe. If both run on one
prompt, the jot is saved once, keyed on Cursor's `generation_id`. Only the hook
that saved it shows `✓ Jotted #N`; the other blocks without a message, so
Cursor doesn't show the confirmation twice. After any change, type `>> test`
and check you get `✓ Jotted #N` rather than a model reply.

On a jot the hook returns `{"continue":false,"user_message":"✓ Jotted #N"}`.
Every other prompt gets `{"continue":true}`. The conversation id is stored as
the session. With a single workspace root, that root is used as the working
directory. With several roots, jotaside doesn't guess: it records all of them in
the entry's metadata and leaves the git fields empty. A multi-root window is a
normal Cursor layout, and every jot captured there has no repo, branch, or
commit.

Verified with Cursor 3.22.12 and, after a restart, 3.23.12, on macOS. This was
before the fix for #23, while the Claude hook was silent on `beforeSubmitPrompt`:

- An ordinary prompt ran `jotaside hook cursor` from `~/.cursor/hooks.json` and
  returned `{"continue":true}` in 16ms, and the model received it.
- A `>>` prompt sent while only the Claude hook was installed ran as
  `jotaside hook claude`, produced no output, and reached the model.
- Replayed with `hook_event_name: "beforeSubmitPrompt"`: a `>>` prompt returns
  `{"continue":false,"user_message":"✓ Jotted #N"}`. One workspace root records
  repo, branch, and commit. Two or more leave the git fields empty and store
  the roots in metadata.
- `>>x`, a leading space, and `>>` mid-sentence pass through.
- Live on 3.23.12 with both hooks installed, `>> hello from cursor` ran
  `jotaside hook cursor`, which returned
  `{"continue":false,"user_message":"✓ Jotted #9"}`. The imported Claude hook
  produced no output, and the prompt still reached the model. `jotaside show 9`
  has `source: cursor`, the conversation id as the session, no repo, branch,
  or commit, and the workspace roots in metadata.
- Live on 3.23.12 with the Claude hook removed, `>> test` returned
  `✓ Jotted #10` and the model did not reply.

Verified live on Cursor 3.23.12, macOS, after the fix for #23. A `>>` prompt was
blocked and saved once in each setup:

- Both hooks: jot #13. Both hooks replied `{"continue":false}` and Cursor
  logged "Merged 2 valid response(s)". That build showed `✓ Jotted #13` twice.
- Both hooks again, with the hook that finds the jot already saved replying
  without a message: jot #17, and `✓ Jotted #17` appeared once.
- Only `jotaside hook claude`, imported by Cursor: jot #14, saved with
  `source: cursor`.
- Only `jotaside hook cursor`: jot #15.
- After the both-hooks and Claude-only runs, the next message in that chat sent
  the blocked jot to the model with it
  (aside-jot #25).
