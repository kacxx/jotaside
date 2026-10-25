#!/usr/bin/env bash
# Re-records docs/demo.gif with the real Claude Code CLI.
#
# Needs: claude (logged in), asciinema 3, agg, go, python3. Builds jotaside from
# this checkout and runs everything in /tmp/jotaside-demo with its own database,
# so no real jots, paths or settings appear. Claude runs with only a demo
# settings file (--setting-sources local), so warnings about your own settings
# stay out of the recording. Each run makes a few real model calls.
#
# In these scripted sessions Claude Code 2.1.301+ puts each jot back in the
# input box after it's saved; drive.py clears it. It hasn't been seen in a
# normal session (checked on 2.1.310, #8).
set -euo pipefail

repo=$(cd "$(dirname "$0")/../.." && pwd)
work=/tmp/jotaside-demo
here="$repo/docs/demo"

rm -rf "$work/api" "$work/bin" "$work"/demo.*
mkdir -p "$work/api" "$work/bin"
(cd "$repo" && go build -o "$work/bin/jotaside" ./cmd/jotaside)

cd "$work/api"
git init -q -b main
printf '# api\n\nToken service: issues short-lived auth tokens and caches them per tenant.\n' > README.md
printf 'package api\n\nimport "time"\n\n// tokenTTL is how long an issued token stays in the cache.\nconst tokenTTL = 24 * time.Hour\n' > cache.go
printf 'module api\n\ngo 1.25.0\n' > go.mod
git add .
git -c user.name=demo -c user.email=demo@example.com commit -qm "token cache"

# Demo-only Claude config: the jotaside hook and MCP server from $work/bin, plus
# the env and model from your user settings (proxy certificates, for example).
python3 - "$work" <<'EOF'
import json, os, sys
work = sys.argv[1]
user = {}
try:
    user = json.load(open(os.path.expanduser("~/.claude/settings.json")))
except OSError:
    pass
settings = {
    "env": user.get("env", {}),
    # Edit is the TTL change, only inside the throwaway tree (macOS reports
    # it as /private/tmp). jotaside done is the only shell command. Anything
    # else has to ask, and the driver treats a permission prompt as a failed take.
    "permissions": {"allow": [
        "mcp__jotaside",
        f"Edit({work}/api/**)",
        f"Edit(/private{work}/api/**)",
        f"Bash({work}/bin/jotaside done *)",
        f"Bash(/private{work}/bin/jotaside done *)",
    ]},
    "hooks": {"UserPromptSubmit": [{"hooks": [
        {"type": "command", "command": f"{work}/bin/jotaside hook claude"}]}]},
}
if "model" in user:
    settings["model"] = user["model"]
json.dump(settings, open(f"{work}/settings.json", "w"), indent=2)
json.dump({"mcpServers": {"jotaside": {"command": f"{work}/bin/jotaside", "args": ["mcp"]}}},
          open(f"{work}/mcp.json", "w"), indent=2)
EOF

if [ ! -x "$work/venv/bin/python" ]; then
  python3 -m venv "$work/venv"
  "$work/venv/bin/pip" -q install pexpect
fi

export JOT_DB="$work/demo.db"
export DEMO_WORK="$work"
export DEMO_REPO="$repo"
"$work/venv/bin/python" "$here/drive.py" --trust
asciinema rec --overwrite --quiet --window-size 100x30 \
  -c "$work/venv/bin/python $here/drive.py" "$work/demo.cast" > /dev/null
# asciinema exits 0 even when the driver raises. A failed take stops here,
# before agg replaces the gif.
if grep -q -e Traceback -e TimeoutError -e "demo:" "$work/demo.cast"; then
  echo "demo driver failed; leaving docs/demo.gif unchanged" >&2
  exit 1
fi
# Claude Code's banner names the API provider after the model ("Opus 5.5 ·
# <provider>"); keep that account detail out of the GIF.
python3 - "$work/demo.cast" <<'EOF'
import json, re, sys
path = sys.argv[1]
lines = open(path).read().splitlines()
# The provider is the words after the model on its own line ("Opus 5.5 ·
# <name>"), each word placed with a cursor move. The same shape is how
# Claude draws "· done 2:50 PM" and the status bar, so a bare "·" plus
# cursor moves is not enough.
provider = re.compile(
    r"((?:Opus|Sonnet|Haiku)\x1b\[\d+G\d+(?:\.\d+)?)"
    r"\x1b\[\d+G·(?:\x1b\[\d+G[^\x1b\r\n]+)+(?=\r)")
model = re.compile(r"(?:Opus|Sonnet|Haiku)\x1b\[\d+G\d+(?:\.\d+)?")
# Ctrl-U (clearing the replayed jot) draws this in the status bar. Blank it
# in place so the column layout of that redraw stays intact.
paste_hint = "Ctrl+Y to paste deleted text"
resume = re.compile(r"(?:Press Ctrl-D again to exit|Resume this session with:)\r*\n(?:claude --resume \S+\r*\n)?")
removed = 0
saw_model = False
for i, line in enumerate(lines[1:], 1):
    ev = json.loads(line)
    if ev[1] != "o":
        continue
    text = ev[2]
    changed = False
    if model.search(text):
        saw_model = True
    if provider.search(text):
        text, n = provider.subn(r"\1", text)
        removed += n
        changed = n > 0
        if provider.search(text):
            sys.exit("demo: provider name is still in the banner; not writing the gif")
    if paste_hint in text:
        text = text.replace(paste_hint, " " * len(paste_hint))
        changed = True
    if resume.search(text):
        text = resume.sub("", text)
        changed = True
    if changed:
        ev[2] = text
        lines[i] = json.dumps(ev, ensure_ascii=False)
if not saw_model:
    sys.exit("demo: model banner was not found; not writing the gif")
open(path, "w").write("\n".join(lines) + "\n")
EOF
agg --theme monokai --speed 1.6 --idle-time-limit 1.5 --last-frame-duration 5 \
  "$work/demo.cast" "$repo/docs/demo.gif" > /dev/null 2>&1

"$work/bin/jotaside" search token
ls -l "$repo/docs/demo.gif"
