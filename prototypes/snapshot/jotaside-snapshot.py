#!/usr/bin/env python3
"""Build a static, read-only page of every jot, its session and where it was jotted.

    python3 prototypes/snapshot/jotaside-snapshot.py --open
    python3 prototypes/snapshot/jotaside-snapshot.py [--out FILE] [--db FILE] [--jotaside PATH]

A prototype for working out what a view of jotaside should show, for user
acceptance testing. It is not part of jotaside and has no tests; see the
README beside it.

It reads nothing from the live database itself. `jotaside backup` writes a
consistent copy into a new temp folder, the script reads every row of that
copy, and runs jotaside's read-only MCP server against the same copy for what
jotaside works out from the transcripts: each session's label, start folder,
resume and open commands, and the open jots' session titles. The temp folder
is removed afterwards. The page goes to ~/.cache/jotaside/jotaside-snapshot.html
unless --out says otherwise, readable by this user only on macOS and Linux (on
Windows it takes the folder's permissions). Nothing else is written and there
are no network calls.

The page holds the jots' text and folder paths, which are private: keep it on
this machine, and don't write it into a repo.

Written for Python 3.8 or later.
"""
import argparse
import datetime
import html
import json
import os
import pathlib
import re
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import webbrowser

DEFAULT_OUT = os.path.join(os.path.expanduser("~"), ".cache", "jotaside", "jotaside-snapshot.html")
HOME = os.path.expanduser("~")
MCP_LIMIT = 200  # the most the MCP server returns from one call

SOURCES = {"claude": "Claude Code", "codex": "Codex", "cursor": "Cursor", "cli": "jotaside add"}
AGE_GROUPS = [  # (label, lowest age in days, css class)
    ("Jotted today", 0, "s-today"),
    ("1 to 6 days old", 1, "s-upcoming"),
    ("7 to 29 days old", 7, "s-waiting"),
    ("30 days or older", 30, "s-overdue"),
]


def esc(s):
    return html.escape(str(s), quote=True)


def find_jotaside(given):
    if given:
        if not os.path.isfile(given):
            sys.exit("no jotaside at " + given)
        return given
    found = shutil.which("jotaside")
    if found:
        return found
    exe = "jotaside.exe" if os.name == "nt" else "jotaside"
    cand = os.path.join(HOME, "go", "bin", exe)
    if os.path.isfile(cand):
        return cand
    sys.exit("jotaside not found: put it on PATH or pass --jotaside")


def run(cmd, env, stdin=None):
    res = subprocess.run(cmd, input=stdin, capture_output=True, text=True, encoding="utf-8",
                         errors="replace", env=env, check=False)
    if res.returncode != 0:
        sys.exit(" ".join(cmd[1:2]) + " failed: " + (res.stderr.strip() or res.stdout.strip()))
    return res.stdout


def read_rows(db):
    con = sqlite3.connect(pathlib.Path(db).as_uri() + "?mode=ro", uri=True)
    try:
        con.row_factory = sqlite3.Row
        rows = [dict(r) for r in con.execute(
            "SELECT id, text, created_at, status, source, session_id, cwd, repo_root, repo_name,"
            " branch, commit_sha, metadata FROM entries ORDER BY id DESC")]
    finally:
        con.close()
    for r in rows:
        try:
            meta = json.loads(r["metadata"] or "{}")
        except ValueError:
            meta = {}
        r["meta"] = meta if isinstance(meta, dict) else {}
        r["created"] = parse_time(r["created_at"])
    return rows


def mcp_calls(exe, env, calls):
    """Runs the read-only MCP server once and returns each tool call's
    structuredContent, in order (None for a call that failed)."""
    reqs = [{"jsonrpc": "2.0", "id": 0, "method": "initialize",
             "params": {"protocolVersion": "2025-11-25", "capabilities": {},
                        "clientInfo": {"name": "jotaside-snapshot", "version": "0"}}},
            {"jsonrpc": "2.0", "method": "notifications/initialized"}]
    for n, (name, args) in enumerate(calls, 1):
        reqs.append({"jsonrpc": "2.0", "id": n, "method": "tools/call",
                     "params": {"name": name, "arguments": args}})
    res = subprocess.run([exe, "mcp"], input="\n".join(json.dumps(r) for r in reqs) + "\n",
                         capture_output=True, text=True, encoding="utf-8", errors="replace", env=env, check=False)
    # A server that fails leaves every result None: the page falls back and says so.
    out = res.stdout if res.returncode == 0 else ""
    by_id = {}
    for line in out.splitlines():
        try:
            resp = json.loads(line)
        except ValueError:
            continue
        if isinstance(resp, dict):
            by_id[resp.get("id")] = resp
    info = (by_id.get(0, {}).get("result") or {}).get("serverInfo") or {}
    results = []
    for n in range(1, len(calls) + 1):
        res = by_id.get(n, {}).get("result") or {}
        results.append(None if res.get("isError") else res.get("structuredContent"))
    return info.get("version", "?"), results


def parse_time(s):
    """created_at and the metadata stamps are UTC, as 2006-01-02T15:04:05.000Z;
    the MCP server writes RFC 3339 with up to nine fraction digits."""
    if not s:
        return None
    m = re.match(r"^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d+))?(Z|[+-]\d{2}:\d{2})$", s)
    if not m:
        return None
    frac = (m.group(2) or "0")[:6].ljust(6, "0")
    tz = "+00:00" if m.group(3) == "Z" else m.group(3)
    try:
        return datetime.datetime.fromisoformat(f"{m.group(1)}.{frac}{tz}")
    except ValueError:
        return None


def local(t):
    return t.astimezone() if t else None


def when(t):
    return local(t).strftime("%Y-%m-%d %H:%M") if t else "?"


def age_days(t, today):
    """Whole calendar days since t, in local time, as jotaside's AgeDays counts
    them. A jot dated after today (a clock ahead) counts as today's, and one
    whose date didn't parse as None."""
    return max(0, (today - local(t).date()).days) if t else None


def short_path(p):
    if p and (p == HOME or p.startswith((HOME + os.sep, HOME + "/"))):
        return "~" + p[len(HOME):]
    return p


def source_label(r):
    if r["source"] == "claude" and str(r["meta"].get("claude_entrypoint", "")).startswith("claude-desktop"):
        return "Claude Desktop"
    return SOURCES.get(r["source"], r["source"] or "unknown")


def place_of(r):
    """Where a jot was made: its repo if it was in one, else its folder."""
    if r["repo_root"]:
        return ("repo", r["repo_root"])
    if r["cwd"]:
        return ("folder", r["cwd"])
    return ("none", "")


def place_title(kind, key, names):
    if kind == "repo":
        name = min(names) if names else os.path.basename(key)
        return f'{esc(name)} <span class="kind">repo</span>'
    if kind == "folder":
        return f'{esc(short_path(key))} <span class="kind">folder, not a repo</span>'
    return '<span class="dim">No folder recorded</span>'


def link_html(url):
    m = re.match(r"^https?://github\.com/([^/]+)/([^/]+)/(issues|pull)/(\d+)", url)
    text = f"{m.group(2)}#{m.group(4)}" if m else re.sub(r"^https?://", "", url)[:40]
    if re.match(r"^https?://", url):
        return f'<a class="link" href="{esc(url)}">{esc(text)}</a>'
    return f'<span class="link plain">{esc(url)}</span>'


def first_line(s):
    return (s or "").split("\n", 1)[0]


def session_name(text):
    """The name a `session:` jot (any case) gives its session, as jotaside's
    sessionName reads it: the first line after the prefix, which may start on
    the next line. "" if text has no such prefix or names nothing."""
    if text[:8].lower() != "session:":
        return ""
    return text[8:].strip().split("\n", 1)[0].strip()


def jot_html(r, today, from_label="", show_place=True):
    done = r["status"] == "done"
    bits = [f'<span class="mono">#{r["id"]}</span>',
            f'<span class="badge {"s-done" if done else "s-upcoming"}">{"Done" if done else "Open"}</span>']
    age = age_days(r["created"], today)
    bits.append(f'<span class="fact" title="{esc(when(r["created"]))}">jotted {esc(when(r["created"]))}'
                + (f", {age} days ago" if age else ", today" if age == 0 else "") + "</span>")
    bits.append(f'<span class="src">{esc(source_label(r))}</span>')
    if show_place:
        kind, key = place_of(r)
        if kind == "repo":
            where = r["repo_name"] + (f' @ {r["branch"]}' if r["branch"] else "")
        else:
            where = short_path(key) if key else "no folder"
        bits.append(f'<span class="fact place" title="{esc(r["cwd"])}">{esc(where)}</span>')
    elif r["branch"]:
        bits.append(f'<span class="fact">@ {esc(r["branch"])}</span>')
    checked = parse_time(r["meta"].get("last_checked_at", "")) if not done else None
    if checked:
        bits.append(f'<span class="fact">checked {esc(when(checked))}</span>')
    elif not done:
        bits.append('<span class="fact dim">never checked</span>')
    if done:
        done_at = parse_time(r["meta"].get("done_at", ""))
        if done_at:
            bits.append(f'<span class="fact">done {esc(when(done_at))}</span>')
    if r["meta"].get("issue_url"):
        bits.append(link_html(str(r["meta"]["issue_url"])))
    if r.get("title"):
        bits.append(f'<span class="fact title" title="the Claude Code session\'s title">'
                    f'&ldquo;{esc(r["title"])}&rdquo;</span>')
    note = r["meta"].get("done_note")
    note_html = f'<div class="note">Done note: {esc(note)}</div>' if note else ""
    # The session's label, unless the label is this jot's own first line.
    show_from = from_label and from_label != first_line(r["text"])
    where = f'<span class="from" title="session">{esc(from_label)}</span>' if show_from else ""
    return (f'<li class="item {"done" if done else "open"}" data-k="{esc(r["k"])}">'
            f'<span class="box" aria-hidden="true"></span><div class="body">'
            f'<div class="desc">{esc(r["text"])}</div>{note_html}'
            f'<div class="facts">{where}{"".join(bits)}</div></div></li>')


def build():
    ap = argparse.ArgumentParser(description="Write a static, read-only page of every jot.")
    ap.add_argument("--out", default=DEFAULT_OUT, help="where to write the page (default: %(default)s)")
    ap.add_argument("--db", help="jot database to read (default: jotaside's own choice, JOT_DB or its data folder)")
    ap.add_argument("--jotaside", help="path to jotaside (default: the one on PATH, else ~/go/bin)")
    ap.add_argument("--open", action="store_true", help="open the page in the default browser")
    args = ap.parse_args()
    exe = find_jotaside(args.jotaside)
    out = os.path.abspath(os.path.expanduser(args.out))

    env = dict(os.environ)
    if args.db:
        if not os.path.isfile(args.db):
            sys.exit("no database at " + args.db)
        env["JOT_DB"] = os.path.abspath(args.db)
    source_db = run([exe, "paths"], env).splitlines()
    source_db = next((ln.split(":", 1)[1].strip() for ln in source_db if ln.startswith("db:")), "?")
    source_db = re.sub(r"\s+\((default|\$[A-Z_]+)\)$", "", source_db)  # `jotaside paths` says where it came from

    tmp = tempfile.mkdtemp(prefix="jotaside-snapshot-")
    try:
        copy = os.path.join(tmp, "jot.db")
        run([exe, "backup", copy], env)
        rows = read_rows(copy)
        env["JOT_DB"] = copy
        version, (sess_res, inbox_res) = mcp_calls(exe, env, [
            ("sessions", {"limit": MCP_LIMIT}), ("inbox", {"limit": MCP_LIMIT})])
    finally:
        shutil.rmtree(tmp, ignore_errors=True)

    mcp_sessions = {(s["source"], s["session_id"]): s for s in (sess_res or {}).get("sessions", [])}
    titles = {e["id"]: e.get("session_title", "") for e in (inbox_res or {}).get("entries", [])}
    today = datetime.datetime.now().astimezone().date()

    # Sessions, newest jot first, as jotaside groups them: by source and id.
    sessions, order = {}, []
    for r in rows:  # rows are newest first
        r["title"] = titles.get(r["id"], "")
        if r["session_id"]:
            key = (r["source"], r["session_id"])
            if key not in sessions:
                sessions[key] = []
                order.append(key)
            sessions[key].append(r)
    loose = [r for r in rows if not r["session_id"]]

    def label_of(key):
        m = mcp_sessions.get(key)
        if m and m.get("label"):
            return m["label"]
        # Past the MCP server's cap: jotaside's rule, as groupSessions applies
        # it. The newest `session:` jot names the session, else the oldest jot.
        for r in sessions[key]:  # newest first
            name = session_name(r["text"])
            if name:
                return name
        return first_line(sessions[key][-1]["text"])

    for r in rows:
        key = (r["source"], r["session_id"])
        r["session_label"] = label_of(key) if r["session_id"] else ""
        # What the filter matches besides the jot's own line.
        r["k"] = " ".join(x for x in (r["session_label"], r["session_id"], r["repo_name"], r["branch"],
                                      r["cwd"], r["repo_root"], r["source"], source_label(r)) if x)

    open_rows = [r for r in rows if r["status"] != "done"]
    n_done = len(rows) - len(open_rows)
    places = {}
    for r in rows:
        places.setdefault(place_of(r), []).append(r)
    old_open = sum(1 for r in open_rows if (age_days(r["created"], today) or 0) >= 30)
    never_checked = sum(1 for r in open_rows if not r["meta"].get("last_checked_at"))
    promoted = sum(1 for r in rows if r["meta"].get("issue_url"))

    tiles = [
        ("Open jots", len(open_rows), ""),
        ("Done", n_done, ""),
        ("Sessions", len(sessions), ""),
        ("Jots not in a session", len(loose), ""),
        ("Repos and folders", len(places), ""),
        ("Open, 30+ days old", old_open, "s-overdue" if old_open else ""),
        ("Open, never checked", never_checked, ""),
        ("Promoted to an issue", promoted, ""),
    ]
    tiles_html = "".join(f'<div class="tile {cls}"><div class="num">{n}</div><div class="lab">{esc(lab)}</div></div>'
                         for lab, n, cls in tiles)

    notes = []
    if sess_res is None or inbox_res is None:
        notes.append("jotaside's MCP server didn't answer, so session labels are worked out here from the jots, "
                     "and start folders, resume and open commands, and titles are missing.")
    elif len(sessions) > len(mcp_sessions):
        notes.append(f"The MCP server returns at most {MCP_LIMIT} sessions: the {len(sessions) - len(mcp_sessions)} "
                     "older ones have labels worked out here from the jots, and no start folder or commands.")
    if len(open_rows) > len(titles) and inbox_res is not None and len(titles) >= MCP_LIMIT:
        notes.append(f"Session titles are shown for the newest {MCP_LIMIT} open jots only.")
    notes_html = "".join(f'<p class="note top">{esc(n)}</p>' for n in notes)

    # View 1: the inbox, by age.
    # A jot whose date didn't parse goes with today's.
    by_group = [[] for _ in AGE_GROUPS]
    for r in open_rows:
        age = age_days(r["created"], today) or 0
        by_group[max(i for i, g in enumerate(AGE_GROUPS) if age >= g[1])].append(r)
    inbox = []
    for (label, _, cls), members in zip(AGE_GROUPS, by_group):
        if members:
            lis = "".join(jot_html(r, today, r["session_label"]) for r in members)
            inbox.append(f'<section class="card"><h2><span class="badge {cls}">{esc(label)}</span> '
                         f'<span class="n">{len(members)}</span></h2><ul class="items">{lis}</ul></section>')

    # View 2: by session.
    cards = []
    for key in order:
        jots = sessions[key]
        m = mcp_sessions.get(key, {})
        n_open = sum(1 for r in jots if r["status"] != "done")
        facts = [f'<span class="src">{esc(source_label(jots[0]))}</span>',
                 f'<span class="mono" title="session id">{esc(key[1])}</span>',
                 f'{n_open} open, {len(jots) - n_open} done',
                 f'last jot {esc(when(jots[0]["created"]))}']
        repo = m.get("repo") or jots[0]["repo_name"]
        if repo:
            facts.append(esc(repo + (f' @ {m.get("branch") or jots[0]["branch"]}' if (m.get("branch") or jots[0]["branch"]) else "")))
        start = m.get("cwd") or jots[-1]["cwd"]
        if start:
            facts.append(f'<span title="{esc(start)}">started in {esc(short_path(start))}</span>')
        title = next((r["title"] for r in jots if r["title"]), "")
        title_html = f'<div class="wsmeta">Session title: &ldquo;{esc(title)}&rdquo;</div>' if title else ""
        cmds = ""
        for what, field in (("Resume", "resume_command"), ("Open the chat", "open_command")):
            if m.get(field):
                cmds += f'<div class="cmd"><span class="dim">{what}:</span> <code>{esc(m[field])}</code></div>'
        lis = "".join(jot_html(r, today, show_place=False) for r in jots)
        cards.append(f'<section class="card"><h2>{esc(label_of(key))}</h2>'
                     f'<div class="wsmeta">{" &middot; ".join(facts)}</div>{title_html}{cmds}'
                     f'<ul class="items">{lis}</ul></section>')
    if loose:
        lis = "".join(jot_html(r, today) for r in loose)
        cards.append('<section class="card"><h2>Not in a session <span class="n">'
                     f'{len(loose)}</span></h2><div class="wsmeta">Jots with no session id, such as those '
                     f'from <code>jotaside add</code>.</div><ul class="items">{lis}</ul></section>')

    # View 3: by repo or folder, with the sessions seen there.
    place_cards = []
    for (kind, key), jots in sorted(places.items(), key=lambda kv: -max(r["id"] for r in kv[1])):
        names = {r["repo_name"] for r in jots if r["repo_name"]}
        branches = sorted({r["branch"] for r in jots if r["branch"]})
        folders = sorted({r["cwd"] for r in jots if r["cwd"]})
        sess = {(r["source"], r["session_id"]) for r in jots if r["session_id"]}
        n_open = sum(1 for r in jots if r["status"] != "done")
        facts = [f'{n_open} open, {len(jots) - n_open} done', f'{len(sess)} session{"s" if len(sess) != 1 else ""}']
        if kind == "repo":
            facts.append(f'<span class="mono">{esc(short_path(key))}</span>')
        if branches:
            facts.append("branches: " + ", ".join(f'<span class="mono">{esc(b)}</span>' for b in branches))
        sub = [f for f in folders if f != key]
        if sub:
            facts.append("jotted from " + ", ".join(f'<span class="mono">{esc(short_path(f))}</span>' for f in sub))
        lis = "".join(jot_html(r, today, r["session_label"], show_place=False) for r in jots)
        place_cards.append(f'<section class="card"><h2>{place_title(kind, key, names)}</h2>'
                           f'<div class="wsmeta">{" &middot; ".join(facts)}</div><ul class="items">{lis}</ul></section>')

    # View 4: every jot as a table, with its metadata.
    trs = []
    for r in rows:
        meta = json.dumps(r["meta"], indent=1, ensure_ascii=False) if r["meta"] else ""
        trs.append(
            f'<tr class="item {"done" if r["status"] == "done" else "open"}" data-k="{esc(r["k"])}">'
            f'<td class="mono">{r["id"]}</td><td>{esc(r["status"])}</td>'
            f'<td class="mono nowrap">{esc(when(r["created"]))}</td><td>{esc(source_label(r))}</td>'
            f'<td class="txt">{esc(r["text"])}</td>'
            f'<td>{esc(r["repo_name"])}{"<br><span class=mono>" + esc(r["branch"]) + "</span>" if r["branch"] else ""}</td>'
            f'<td class="mono path">{esc(short_path(r["cwd"]))}</td>'
            f'<td class="mono">{esc(r["session_id"][:8])}</td>'
            f'<td>{"<details><summary>" + str(len(r["meta"])) + " keys</summary><pre>" + esc(meta) + "</pre></details>" if meta else ""}</td></tr>')
    table = ('<div class="tablewrap"><table><thead><tr><th>#</th><th>Status</th><th>Jotted</th><th>From</th>'
             '<th>Text</th><th>Repo</th><th>Folder</th><th>Session</th><th>Metadata</th></tr></thead>'
             f'<tbody>{"".join(trs)}</tbody></table></div>') if trs else '<p class="dim">No jots.</p>'

    now = datetime.datetime.now().strftime("%Y-%m-%d %H:%M")
    values = {
        "NOW": esc(now), "VERSION": esc(version), "PATH": esc(short_path(source_db)),
        "TILES": tiles_html, "NOTES": notes_html,
        "INBOX": "".join(inbox) or '<p class="dim">The inbox is empty.</p>',
        "SESSIONS": "".join(cards) or '<p class="dim">No jots.</p>',
        "PLACES": "".join(place_cards) or '<p class="dim">No jots.</p>',
        "TABLE": table,
    }
    # One pass over the template only, so a `{{TABLE}}` in a jot is left as text.
    page = re.sub(r"\{\{([A-Z]+)\}\}", lambda m_: values[m_.group(1)], TEMPLATE)
    os.makedirs(os.path.dirname(out), exist_ok=True)
    # The page holds private notes: written to a new file beside the target
    # (O_EXCL, mode 0600: this user only on macOS and Linux) and moved into
    # place, so a failed write leaves the old page whole and a symlink at the
    # target is replaced rather than followed.
    part = f"{out}.{os.getpid()}.tmp"
    try:
        fd = os.open(part, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as fh:
            fh.write(page)
        os.replace(part, out)
    except OSError as e:
        try:
            os.unlink(part)
        except OSError:
            pass
        sys.exit(f"couldn't write {out}: {e}")
    print(f"wrote {out}")
    print(f"{len(rows)} jots ({len(open_rows)} open), {len(sessions)} sessions, {len(loose)} not in a session, "
          f"{len(places)} repos and folders")
    for n in notes:
        print("note: " + n)
    if args.open:
        webbrowser.open(pathlib.Path(out).as_uri())


TEMPLATE = """<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Jotaside snapshot</title>
<style>
:root {
  --bg: #f6f6f3; --surface: #ffffff; --text: #1f2328; --muted: #667085; --border: #e3e3de;
  --accent: #2f6f4f; --code: #f0f0ec;
  --overdue: #b42318; --overdue-bg: #fee4e2; --today: #b54708; --today-bg: #fef0c7;
  --waiting: #175cd3; --waiting-bg: #dbe8fe;
  --upcoming: #067647; --upcoming-bg: #d7f2e3; --done: #667085; --done-bg: #f0f0ec;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #15171a; --surface: #1e2125; --text: #e6e8eb; --muted: #98a2b3; --border: #30343a;
    --accent: #6fcf97; --code: #272b30;
    --overdue: #fda29b; --overdue-bg: #4a1d19; --today: #fec84b; --today-bg: #47310b;
    --waiting: #84adff; --waiting-bg: #1b2f55;
    --upcoming: #75e0a7; --upcoming-bg: #123d29; --done: #98a2b3; --done-bg: #272b30;
  }
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--text);
  font: 15px/1.5 system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
main { max-width: 1040px; margin: 0 auto; padding: 28px 16px 60px; }
h1 { font-size: 24px; margin: 0 0 4px; }
.sub { color: var(--muted); font-size: 13px; margin-bottom: 20px; overflow-wrap: anywhere; }
.tiles { display: grid; grid-template-columns: repeat(auto-fill, minmax(140px, 1fr)); gap: 10px; margin-bottom: 22px; }
.tile { background: var(--surface); border: 1px solid var(--border); border-radius: 10px; padding: 12px 14px; }
.tile .num { font-size: 26px; font-weight: 650; line-height: 1.1; font-variant-numeric: tabular-nums; }
.tile .lab { color: var(--muted); font-size: 12.5px; margin-top: 2px; }
.tile.s-overdue { background: var(--surface); } .tile.s-overdue .num { color: var(--overdue); }
.tabs { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 16px; border-bottom: 1px solid var(--border); }
.tabs button { font: inherit; color: var(--muted); background: none; border: 0; padding: 8px 12px;
  border-bottom: 2px solid transparent; cursor: pointer; margin-bottom: -1px; }
.tabs button[aria-selected="true"] { color: var(--text); border-bottom-color: var(--accent); font-weight: 600; }
.tabs button:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; border-radius: 4px; }
.view[hidden], .item[hidden], .card[hidden] { display: none; }
.filter { display: flex; flex-wrap: wrap; gap: 10px; align-items: center; margin-bottom: 14px; }
.filter input[type="search"] { flex: 1 1 260px; font: inherit; color: var(--text); background: var(--surface);
  border: 1px solid var(--border); border-radius: 8px; padding: 8px 12px; }
.filter input[type="search"]:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.filter label { color: var(--muted); font-size: 13px; display: flex; gap: 5px; align-items: center; }
.filter .hits { color: var(--muted); font-size: 13px; }
.card { background: var(--surface); border: 1px solid var(--border); border-radius: 10px;
  padding: 14px 16px; margin-bottom: 12px; }
.card h2 { font-size: 16.5px; margin: 0 0 4px; overflow-wrap: anywhere; }
.card h2 .n { color: var(--muted); font-weight: 400; font-size: 14px; }
.kind { color: var(--muted); font-weight: 400; font-size: 12.5px; }
.wsmeta { color: var(--muted); font-size: 13px; margin-bottom: 6px; overflow-wrap: anywhere; }
.cmd { font-size: 12.5px; margin-bottom: 4px; overflow-wrap: anywhere; }
.cmd code { background: var(--code); padding: 1px 5px; border-radius: 4px; }
.mono, code, pre { font-family: ui-monospace, "Cascadia Mono", Consolas, monospace; font-size: 12.5px; }
ul.items { list-style: none; margin: 6px 0 0; padding: 0; }
li.item { display: flex; gap: 10px; padding: 8px 0; border-top: 1px solid var(--border); }
li.item .box { flex: none; width: 15px; height: 15px; margin-top: 4px; border: 1.5px solid var(--muted); border-radius: 3px; }
li.item.done .box { background: var(--muted); box-shadow: inset 0 0 0 2.5px var(--surface); }
li.item.done .desc { color: var(--muted); }
.body { min-width: 0; } .desc { overflow-wrap: anywhere; white-space: pre-wrap; }
.note { font-size: 13px; color: var(--muted); margin-top: 2px; }
.note.top { background: var(--today-bg); color: var(--today); padding: 8px 12px; border-radius: 8px; }
.facts { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; margin-top: 3px; font-size: 12.5px; color: var(--muted); }
.badge { display: inline-block; padding: 1px 8px; border-radius: 999px; font-size: 12px; font-weight: 600; white-space: nowrap; }
.s-overdue { color: var(--overdue); background: var(--overdue-bg); } .s-today { color: var(--today); background: var(--today-bg); }
.s-waiting { color: var(--waiting); background: var(--waiting-bg); }
.s-upcoming { color: var(--upcoming); background: var(--upcoming-bg); } .s-done { color: var(--done); background: var(--done-bg); }
.src { font-size: 11.5px; background: var(--code); padding: 0 6px; border-radius: 4px; white-space: nowrap; }
.from { font-weight: 600; color: var(--text); overflow-wrap: anywhere; }
.from::after { content: "\\00b7"; margin-left: 6px; color: var(--muted); font-weight: 400; }
.title { font-style: italic; }
.link { color: var(--accent); text-decoration: none; } a.link:hover { text-decoration: underline; }
.dim { color: var(--muted); }
.tablewrap { overflow-x: auto; background: var(--surface); border: 1px solid var(--border); border-radius: 10px; }
table { border-collapse: collapse; width: 100%; font-size: 13px; }
th, td { text-align: left; vertical-align: top; padding: 7px 10px; border-top: 1px solid var(--border); }
th { color: var(--muted); font-weight: 600; border-top: 0; white-space: nowrap; }
td.txt { min-width: 220px; white-space: pre-wrap; overflow-wrap: anywhere; }
td.path { min-width: 160px; max-width: 260px; overflow-wrap: anywhere; }
.nowrap { white-space: nowrap; }
tr.done td { color: var(--muted); }
details pre { margin: 4px 0 0; white-space: pre-wrap; overflow-wrap: anywhere; max-width: 320px; }
summary { cursor: pointer; color: var(--accent); }
footer { color: var(--muted); font-size: 12.5px; margin-top: 26px; }
</style>
</head>
<body>
<main>
  <h1>Jotaside snapshot</h1>
  <div class="sub">Snapshot taken {{NOW}} &middot; jotaside {{VERSION}} &middot; {{PATH}}</div>
  <div class="tiles">{{TILES}}</div>
  {{NOTES}}
  <div class="filter">
    <input id="q" type="search" placeholder="Filter: words, a repo, a folder, a session; a comma for any of (a, b)" aria-label="Filter jots" autocomplete="off">
    <label><input type="checkbox" id="opendone"> Hide done jots</label>
    <span class="hits" id="hits" aria-live="polite"></span>
  </div>
  <div class="tabs" role="tablist">
    <button role="tab" aria-selected="true" data-view="inbox">Inbox</button>
    <button role="tab" aria-selected="false" data-view="sessions">By session</button>
    <button role="tab" aria-selected="false" data-view="places">By repo or folder</button>
    <button role="tab" aria-selected="false" data-view="all">Every jot</button>
  </div>
  <div class="view" id="inbox">{{INBOX}}</div>
  <div class="view" id="sessions" hidden>{{SESSIONS}}</div>
  <div class="view" id="places" hidden>{{PLACES}}</div>
  <div class="view" id="all" hidden>{{TABLE}}</div>
  <footer>Read-only snapshot of a <code>jotaside backup</code> copy. Session labels, start folders, resume and open
  commands, and session titles come from jotaside's MCP server; nothing was written to the jot database.
  Private: this page holds the jots' text and folder paths.</footer>
</main>
<script>
document.querySelectorAll('.tabs button').forEach(function (b) {
  b.addEventListener('click', function () {
    document.querySelectorAll('.tabs button').forEach(function (o) { o.setAttribute('aria-selected', o === b ? 'true' : 'false'); });
    document.querySelectorAll('.view').forEach(function (v) { v.hidden = v.id !== b.dataset.view; });
  });
});

// Filter, in every view. A jot's text is its own line (text, facts, links)
// plus its session's label and id, its repo, branch, folder and source, so
// every view matches the same jots. The query is any-of clauses split on `,`
// or `|`; within a clause, every word must appear at the start of a word
// (case-insensitive). Card headings aren't matched.
(function () {
  var q = document.getElementById('q'), hide = document.getElementById('opendone'), hits = document.getElementById('hits');
  function textOf(el) {
    if (el._q === undefined) {
      var walk = document.createTreeWalker(el, NodeFilter.SHOW_TEXT), parts = [];
      while (walk.nextNode()) { parts.push(walk.currentNode.nodeValue); }
      el._q = (parts.join(' ') + ' ' + (el.dataset.k || '')).toLowerCase();
    }
    return el._q;
  }
  function atWordStart(text, w) {
    for (var i = text.indexOf(w); i !== -1; i = text.indexOf(w, i + 1)) {
      if (i === 0 || !/[a-z0-9]/.test(text.charAt(i - 1))) { return true; }
    }
    return false;
  }
  function has(text, clauses) {
    return clauses.some(function (terms) {
      return terms.every(function (w) { return atWordStart(text, w); });
    });
  }
  function apply() {
    var clauses = q.value.toLowerCase().split(/[,|]/).map(function (c) {
      return c.split(/\\s+/).filter(Boolean);
    }).filter(function (c) { return c.length; });
    var on = clauses.length > 0, hideDone = hide.checked, shown = 0, open = 0;
    document.querySelectorAll('.item').forEach(function (el) {
      var hit = (!on || has(textOf(el), clauses)) && !(hideDone && el.classList.contains('done'));
      el.hidden = !hit;
      if (hit && el.tagName === 'TR') { shown++; if (el.classList.contains('open')) { open++; } }
    });
    document.querySelectorAll('.card').forEach(function (card) {
      card.hidden = (on || hideDone) && !card.querySelector('li.item:not([hidden])');
    });
    hits.textContent = (on || hideDone) ? shown + (shown === 1 ? ' jot matches' : ' jots match') + ' (' + open + ' open)' : '';
  }
  q.addEventListener('input', apply);
  hide.addEventListener('change', apply);
})();
</script>
</body>
</html>
"""

if __name__ == "__main__":
    build()
