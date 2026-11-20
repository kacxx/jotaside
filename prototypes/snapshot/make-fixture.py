#!/usr/bin/env python3
"""Make a throwaway jot database for trying jotaside-snapshot.py without real jots.

    python3 prototypes/snapshot/make-fixture.py "$(mktemp -d)"
    python3 prototypes/snapshot/jotaside-snapshot.py --db <that folder>/fixture.db --open

It writes OUTDIR/fixture.db, with the schema of internal/store/store.go, and
fake Claude Code transcripts in OUTDIR/transcripts for the jots' session
titles and start folders. The jots cover open and done, every source, a
`session:` label, a done note, a promoted jot, a checked jot, jots outside a
repo or a session, and text that must show as text. The folders they name,
under your home folder, don't need to exist. It writes nothing else.
"""
import datetime
import json
import os
import sqlite3
import sys

if len(sys.argv) != 2:
    sys.exit("usage: make-fixture.py OUTDIR")
out = os.path.abspath(sys.argv[1])
os.makedirs(out, exist_ok=True)
db = os.path.join(out, "fixture.db")
if os.path.exists(db):
    os.remove(db)
tdir = os.path.join(out, "transcripts")
os.makedirs(tdir, exist_ok=True)

SCHEMA = """
CREATE TABLE IF NOT EXISTS entries (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	text       TEXT NOT NULL,
	created_at TEXT NOT NULL,
	status     TEXT NOT NULL DEFAULT 'inbox',
	source     TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	cwd        TEXT NOT NULL DEFAULT '',
	repo_root  TEXT NOT NULL DEFAULT '',
	repo_name  TEXT NOT NULL DEFAULT '',
	branch     TEXT NOT NULL DEFAULT '',
	commit_sha TEXT NOT NULL DEFAULT '',
	metadata   TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata))
);
CREATE INDEX IF NOT EXISTS entries_status_id ON entries(status, id);
PRAGMA user_version = 1;
"""

now = datetime.datetime.now(datetime.timezone.utc)  # noqa: UP017 (datetime.UTC is 3.11)
HOME = os.path.expanduser("~")
code = os.path.join(HOME, "code")
alpha = os.path.join(code, "alpha-service")
beta = os.path.join(code, "beta-web")
notes = os.path.join(HOME, "notes")


def ts(days, hours=0):
    t = now - datetime.timedelta(days=days, hours=hours)
    return t.strftime("%Y-%m-%dT%H:%M:%S.") + f"{t.microsecond // 1000:03d}Z"


def transcript(name, start_cwd, title=None, custom=None):
    p = os.path.join(tdir, name + ".jsonl")
    with open(p, "w", encoding="utf-8") as fh:
        fh.write(json.dumps({"type": "user", "cwd": start_cwd, "message": {"content": "hi"}}) + "\n")
        if title:
            fh.write(json.dumps({"type": "ai-title", "aiTitle": title}) + "\n")
        if custom:
            fh.write(json.dumps({"type": "custom-title", "customTitle": custom}) + "\n")
    return p


ta = transcript("aaaa1111", alpha, title="Retry budget for the queue worker")
tb = transcript("bbbb2222", beta, title="Header layout", custom="Header redesign")
tc = transcript("cccc3333", code)  # started one folder up, no title
td = transcript("dddd4444", alpha, title="Desktop chat about alerts")

S_A = "aaaa1111-0000-4000-8000-000000000001"
S_B = "bbbb2222-0000-4000-8000-000000000002"
S_C = "cccc3333-0000-4000-8000-000000000003"
S_D = "dddd4444-0000-4000-8000-000000000004"
S_X = "019a0000-codex-thread-0000000000005"

rows = [
    # text, days ago, status, source, session, cwd, repo_root, repo_name, branch, meta
    ("Check whether the retry budget resets on deploy", 41, "inbox", "claude", S_A, alpha, alpha, "alpha-service", "main",
     {"transcript_path": ta, "model": "m"}),
    ("session: ABC-1290 queue worker retries", 41, "inbox", "claude", S_A, alpha, alpha, "alpha-service", "main",
     {"transcript_path": ta}),
    ("Ask about the dead-letter alarm\nsecond line of the same jot", 12, "inbox", "claude", S_A,
     os.path.join(alpha, "worker"), alpha, "alpha-service", "fix/retries",
     {"transcript_path": ta, "last_checked_at": ts(2)}),
    ("Header jumps on narrow screens", 9, "done", "claude", S_B, beta, beta, "beta-web", "feat/header",
     {"transcript_path": tb, "done_at": ts(3), "done_note": "fixed in the header PR"}),
    ("<script>alert('x')</script> and {{TABLE}} should show as text", 3, "inbox", "claude", S_B, beta, beta, "beta-web",
     "feat/header", {"transcript_path": tb}),
    ("Promote: icon contrast in dark mode", 2, "done", "claude", S_B, beta, beta, "beta-web", "feat/header",
     {"transcript_path": tb, "done_at": ts(1), "issue_url": "https://github.com/example/beta-web/issues/42"}),
    ("jotted after cd into the repo", 1, "inbox", "claude", S_C, alpha, alpha, "alpha-service", "main",
     {"transcript_path": tc}),
    ("Alert routing question from the desktop app", 0, "inbox", "claude", S_D, alpha, alpha, "alpha-service", "main",
     {"transcript_path": td, "claude_entrypoint": "claude-desktop", "claude_desktop_session_id": "local_00000000-0000-4000-8000-00000000000d"}),
    ("Codex: look at flaky test in parser", 5, "inbox", "codex", S_X, beta, beta, "beta-web", "main",
     {"turn_id": "t1"}),
    ("Codex: parser follow-up", 5, "done", "codex", S_X, beta, beta, "beta-web", "main", {"done_at": ts(4)}),
    ("Buy more coffee", 35, "inbox", "cli", "", notes, "", "", "", {}),
    ("Read the release notes", 0, "inbox", "cli", "", "", "", "", "", {}),
    ("Odd metadata row", 20, "done", "cli", "", notes, "", "", "", [1, 2]),
]

con = sqlite3.connect(db)
con.executescript(SCHEMA)
for text, days, status, source, sid, cwd, root, name, branch, meta in rows:
    con.execute("INSERT INTO entries (text, created_at, status, source, session_id, cwd, repo_root, repo_name,"
                " branch, commit_sha, metadata) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
                (text, ts(days, 1), status, source, sid, cwd, root, name, branch, "", json.dumps(meta)))
con.commit()
con.close()
print(db)
