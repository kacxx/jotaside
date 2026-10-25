"""Drive a real Claude Code session for docs/demo.gif. Run by record.sh.

Under `asciinema rec`, the session's output is copied to stdout, which
asciinema records. With --trust it records nothing: it starts Claude Code once
to accept the folder trust dialog, then exits.
"""
import os
import re
import subprocess
import sys
import time

import pexpect

ROWS, COLS = 30, 100
WORK = os.environ["DEMO_WORK"]
REPO = os.environ["DEMO_REPO"]
# acceptEdits, not auto: auto mode approves whatever the model tries, which
# records as a cold session (sed, go vet, a commit offer) instead of the
# plugin's jotaside tools. The plugin and MCP server are loaded for the session.
CLAUDE = (f"command claude --setting-sources local --settings {WORK}/settings.json "
          f"--permission-mode acceptEdits --strict-mcp-config --mcp-config {WORK}/mcp.json "
          f"--plugin-dir {REPO}")
TITLE = r"\033[1mjotaside\033[0m \342\200\224 a side channel for thoughts while you work with coding agents\n\n"

# Terminal queries Claude Code sends at startup, with the replies a terminal
# gives; without them it ignores keys.
REPLIES = {b"\x1b[c": b"\x1b[?62;22c", b"\x1b[?u": b"\x1b[?0u",
           b"\x1b[>0q": b"\x1bP>|xterm(388)\x1b\\"}

trust_only = "--trust" in sys.argv

with open(f"{WORK}/rc.sh", "w") as f:
    f.write(f"alias claude='{CLAUDE}'\nPS1='$ '\nclear\nprintf '{TITLE}'\n")

env = dict(os.environ)
# Drop directories that contain gopls. Claude offers to install the gopls
# plugin the moment it edits a .go file, and that dialog is the "plugins
# aren't installed" screen.
path = [part for part in env["PATH"].split(":")
        if not os.path.isfile(os.path.join(part, "gopls"))]
env["PATH"] = f"{WORK}/bin:" + ":".join(path)
env["TERM"] = "xterm-256color"
env["BASH_SILENCE_DEPRECATION_WARNING"] = "1"

p = pexpect.spawn("/bin/bash", ["--noprofile", "--rcfile", f"{WORK}/rc.sh", "-i"],
                  cwd=f"{WORK}/api", env=env, dimensions=(ROWS, COLS))
recording = not trust_only
seen = b""


def read(timeout):
    """Read what's available, copy it to the recording, answer queries."""
    global seen
    try:
        chunk = p.read_nonblocking(65536, timeout=timeout)
    except pexpect.TIMEOUT:
        return b""
    if recording:
        sys.stdout.buffer.write(chunk)
        sys.stdout.buffer.flush()
    for q, r in REPLIES.items():
        if q in chunk:
            p.send(r)
    seen = (seen + chunk)[-20000:]
    return chunk


def wait_for(pattern, timeout=60):
    """Wait for pattern in output from now on. Returns the index that matched."""
    global seen
    rxs = [re.compile(x) for x in (pattern if isinstance(pattern, list) else [pattern])]
    seen = b""
    end = time.time() + timeout
    while time.time() < end:
        read(0.2)
        for i, rx in enumerate(rxs):
            if rx.search(seen):
                return i
    raise TimeoutError(pattern)


def idle(quiet=2.5, limit=120):
    """Wait until nothing is printed for `quiet` seconds."""
    end = time.time() + limit
    last = time.time()
    while time.time() < end:
        if read(0.2):
            last = time.time()
        elif time.time() - last >= quiet:
            return


# Claude prints a line like "Crunched for 26s · done 1:52 PM" when a turn
# ends. It sometimes draws that line one word at a time, with cursor moves
# between the words, so the match has to ignore those moves. Silence alone is
# not the end of the turn: "esc to interrupt" stays up through silent thinking.
ANSI = re.compile(rb"\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07]*(?:\x07|\x1b\\)|\x1b.")
# "for 13s · done", "for 1m 12s · done", and the same with the spaces
# removed by cursor-move drawing.
DONE = re.compile(rb"for\s*(?:\d+m\s*)?\d+s.{0,8}done")


def visible(buf):
    return ANSI.sub(b"", buf)


def turn_done(limit=240):
    """Wait until Claude's turn has finished."""
    wait_for(rb"interrupt", timeout=45)
    end = time.time() + limit
    last = time.time()
    while time.time() < end:
        if read(0.2):
            last = time.time()
        screen = visible(seen)
        if b"LSP plugin" in screen or b"install this LSP" in screen:
            p.send("2")  # "No, not now" — do not persist a disable on the user account
            raise RuntimeError("LSP plugin recommendation appeared")
        # Any permission prompt fails the take. Match its heading as well as
        # the "don't ask again" option, which 2.1.310 writes with a curly
        # apostrophe and a plain Yes/No prompt doesn't have.
        if (b"Do you want to proceed?" in screen or b"requires approval" in screen
                or b"don't ask again" in screen or "don\u2019t ask again".encode() in screen):
            raise RuntimeError("permission prompt in the recording")
        if time.time() - last >= 1.5 and DONE.search(screen):
            return
    raise TimeoutError("turn")


def pause(t):
    end = time.time() + t
    while time.time() < end:
        read(0.1)


def typed(text, delay=0.045):
    for ch in text:
        p.send(ch)
        pause(delay)


def enter():
    p.send("\r")


def quit_claude():
    # Two Ctrl-Ds, then Ctrl-L. /exit opens the slash-command menu and leaves
    # it on screen; Ctrl-L clears the resume id before the inbox is typed.
    p.send("\x04")
    pause(0.5)
    p.send("\x04")
    wait_for(rb"\$ ", timeout=30)
    p.send("\x0c")
    pause(0.4)


wait_for(rb"\$ ")

if trust_only:
    p.sendline("claude")
    # Claude Code draws each word separately, so match single words.
    if wait_for([rb"confirm", rb"effort"]) == 0:
        pause(1.5)  # keys sent while the dialog is still drawing are dropped
        p.send("\x1b[B")  # "Yes, I trust this folder"
        pause(1.0)
        enter()
        wait_for(rb"effort")
    idle(quiet=1.5, limit=20)
    quit_claude()
    p.sendline("exit")
    p.expect(pexpect.EOF, timeout=10)
    sys.exit(0)

pause(1.5)
typed("claude")
enter()
wait_for(rb"effort")
idle(quiet=1.5, limit=20)
pause(0.5)

def jot(text):
    typed(text)
    pause(0.6)
    enter()
    wait_for(rb"Jotted", timeout=30)
    pause(0.8)  # leave the "✓ Jotted" line readable
    # Scripted sessions on Claude Code 2.1.301+ put the jot back in the input
    # box (#8; not seen in a normal session on 2.1.310); clear it so the next
    # prompt isn't appended to it. Don't sit on the "Ctrl+Y to paste deleted
    # text" hint that Ctrl-U raises.
    p.send("\x15")
    pause(0.4)


def file_has_hour():
    with open(f"{WORK}/api/cache.go") as f:
        return "1 * time.Hour" in f.read()


jot(">> lower the token cache TTL to 1 hour")
jot(">> ask infra whether tenants can override the TTL")

typed("change tokenTTL in cache.go to 1 * time.Hour")
pause(0.4)
enter()
turn_done()
if not file_has_hour():
    sys.stderr.write("demo: cache.go was not edited; not writing the gif\n")
    sys.exit(1)
pause(1.2)

typed("update jot 1")
pause(0.4)
enter()
turn_done()
pause(1.5)

typed("yes, run it. don't commit")
pause(0.4)
enter()
turn_done()
done = subprocess.run([f"{WORK}/bin/jotaside", "search", "token"],
                      capture_output=True, text=True, env=env)
if "(done)" not in done.stdout:
    sys.stderr.write("demo: jot 1 is still open; not writing the gif\n")
    sys.stderr.write(done.stdout)
    sys.exit(1)
pause(1.2)

quit_claude()
pause(1.0)

for cmd, wait in [("jotaside inbox", 2.5),
                  ("jotaside search token", 3.0)]:
    typed(cmd)
    pause(0.3)
    enter()
    wait_for(rb"\$ ", timeout=15)
    pause(wait)

p.sendline("exit")
p.expect(pexpect.EOF, timeout=10)
