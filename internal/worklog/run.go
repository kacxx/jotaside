package worklog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kacxx/jotaside/internal/app"
)

const usage = `worklog links a markdown worklog's items to the sessions that wrote them.

Usage:
  worklog add <workstream> <text>   add an open item and commit it
  worklog close <item or key>       mark the one matching open item done and commit it
  worklog show <item or key>        the matching items, each with its sessions
  worklog brief                     every open item with its sessions

The log's grammar is in docs/worklog-format.md. A workstream is named by its id, or
by its title or part of either. An item's facts follow " — ":

  worklog add outage-followup "Write the retro — due 2026-09-02 · ABC-1290"
  worklog add outage-followup "Guard on runtime processing — @alex · ABC-1290"

add writes the owner (me, or the person with since <today>). close writes
done <today>, drops me and the due date, and keeps the rest. Both add the
calling session's stamp (chat:<first 8 of its ID>) as one more fact, so run
them from the session doing the work, and both refuse to write if the log's
git repo has a remote.

A session is found from the stamp on the item, then from jotaside's find for
each work item the line names. Each is printed with a command that resumes it.
brief lists a session stamped on more than one open item once, after the items.

Environment:
  WORKLOG_LEDGER            the log (default: ~/worklog/ledger.md)
  CLAUDE_CODE_SESSION_ID    the calling session's ID, set by Claude Code
  CURSOR_CONVERSATION_ID    the same in Cursor, used when the first isn't set
  CLAUDE_CONFIG_DIR         Claude Code's folder, for transcripts (default: ~/.claude)
  JOT_DB                    jotaside's database, for the key fallback
`

// Env is what a command reads from its surroundings; tests replace it.
type Env struct {
	Getenv func(string) string
	Home   string
	// Now is the clock, for the dates add and close write; nil means time.Now.
	Now func() time.Time
	// Find, FindEach and Sessions are jotaside's; nil when its database
	// isn't there. FindEach finds many keys in one read of the transcripts;
	// without it, each key is a Find.
	Find     func(ctx context.Context, q string) ([]app.Session, []app.Entry, error)
	FindEach func(ctx context.Context, qs []string) ([]app.Found, error)
	Sessions func(ctx context.Context) ([]app.Session, error)
}

// Run runs one worklog command and returns its exit code.
func Run(ctx context.Context, args []string, env Env, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if err := run(ctx, args[0], args[1:], env, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "worklog:", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, cmd string, args []string, env Env, stdout, stderr io.Writer) error {
	file := env.Getenv("WORKLOG_LEDGER")
	if file == "" {
		file = filepath.Join(env.Home, "worklog", "ledger.md")
	}
	claude := env.Getenv("CLAUDE_CONFIG_DIR")
	if claude == "" {
		claude = filepath.Join(env.Home, ".claude")
	}
	look := &Lookup{Transcripts: filepath.Join(claude, "projects"), Find: env.Find, FindEach: env.FindEach, Sessions: env.Sessions}
	session := env.Getenv("CLAUDE_CODE_SESSION_ID")
	if stampFact(session) == "" {
		session = env.Getenv("CURSOR_CONVERSATION_ID")
	}
	now := time.Now
	if env.Now != nil {
		now = env.Now
	}
	today := now().Format("2006-01-02")

	switch cmd {
	case "add":
		if len(args) < 2 {
			return errors.New("usage: worklog add <workstream> <text>")
		}
		text := strings.Join(args[1:], " ")
		return write(file, session, stderr, "add: "+text, func(content string) (string, string, error) {
			out, err := Add(content, args[0], text, session, today)
			return out, "Added to " + args[0] + ".", err
		}, stdout)
	case "close":
		if len(args) == 0 {
			return errors.New("usage: worklog close <item or key>")
		}
		q := strings.Join(args, " ")
		return write(file, session, stderr, "close: "+q, func(content string) (string, string, error) {
			out, it, err := Close(content, q, session, today)
			return out, "Closed: " + it.Text, err
		}, stdout)
	case "show":
		if len(args) == 0 {
			return errors.New("usage: worklog show <item or key>")
		}
		q := strings.Join(args, " ")
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		items := Find(string(content), q)
		if len(items) == 0 {
			fmt.Fprintf(stdout, "No item matches %q.\n", q)
			return nil
		}
		look.Prefetch(ctx, items)
		printItems(stdout, items, look.ForEach(ctx, items), nil)
		return nil
	case "brief":
		content, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var open []Item
		for _, it := range Items(string(content)) {
			if it.Open {
				open = append(open, it)
			}
		}
		if len(open) == 0 {
			fmt.Fprintln(stdout, "No open items.")
			return nil
		}
		look.Prefetch(ctx, open)
		refs := look.ForEach(ctx, open)
		shared := sharedStamps(refs)
		printItems(stdout, open, refs, shared)
		printShared(stdout, shared)
		return nil
	default:
		return fmt.Errorf("unknown command %q; run worklog help", cmd)
	}
}

// printItems prints items under their workstreams, each with its sessions
// (refs[i] for items[i]) except the stamped ones in shared, which are listed
// once by printShared.
func printItems(w io.Writer, items []Item, refs [][]Ref, shared []sharedStamp) {
	hide := map[string]bool{}
	for _, s := range shared {
		hide[s.ref.group()] = true
	}
	ws := "\x00"
	for i, it := range items {
		name := workstream{title: it.Workstream, id: it.WorkstreamID}.name()
		if name != ws {
			if ws != "\x00" {
				fmt.Fprintln(w)
			}
			ws = name
			if ws != "" {
				fmt.Fprintln(w, ws)
			}
		}
		mark := "[ ]"
		if !it.Open {
			mark = "[x]"
		}
		fmt.Fprintf(w, "  %s %s\n", mark, it.Text)
		if p := it.Problem(); p != "" {
			fmt.Fprintf(w, "      line %d doesn't parse: %s\n", it.Line+1, p)
		}
		if len(refs[i]) == 0 {
			fmt.Fprintln(w, "      no session found")
		}
		for _, r := range refs[i] {
			if r.Stamp && hide[r.group()] {
				continue
			}
			fmt.Fprintf(w, "      %s\n", r.line(0))
		}
	}
}

// sharedStamp is a session stamped on more than one of the items, or a stamp
// on more than one that matches no session, with the number of items.
type sharedStamp struct {
	ref Ref
	n   int
}

// sharedStamps returns what printShared lists: the stamped sessions on more
// than one item, in the order first seen. A coordinating session can be
// stamped on dozens of items, and its resume command is the same on each (#47).
func sharedStamps(refs [][]Ref) []sharedStamp {
	var order []string
	first := map[string]Ref{}
	count := map[string]int{}
	for _, item := range refs {
		for _, r := range item {
			if !r.Stamp {
				continue
			}
			g := r.group()
			if _, ok := first[g]; !ok {
				first[g] = r
				order = append(order, g)
			}
			count[g]++
		}
	}
	var out []sharedStamp
	for _, g := range order {
		if count[g] > 1 {
			out = append(out, sharedStamp{first[g], count[g]})
		}
	}
	return out
}

// printShared lists the sessions stamped on several items, once each.
func printShared(w io.Writer, shared []sharedStamp) {
	if len(shared) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Sessions stamped on several open items")
	for _, s := range shared {
		fmt.Fprintf(w, "  %s\n", s.ref.line(s.n))
	}
}

// write applies edit to the log and commits it. The log must be in a git
// repo with no remote and have no uncommitted changes of its own; if the
// commit fails, the log is put back as it was.
func write(file, session string, stderr io.Writer, msg string, edit func(string) (string, string, error), stdout io.Writer) error {
	dir := filepath.Dir(file)
	if _, err := git(dir, "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("%s isn't in a git repo, so nothing was written", file)
	}
	remotes, err := git(dir, "remote")
	if err != nil {
		return err
	}
	if strings.TrimSpace(remotes) != "" {
		return fmt.Errorf("the log's git repo has a remote (%s), so nothing was written: the log stays local", strings.Join(strings.Fields(remotes), ", "))
	}
	dirty, err := git(dir, "status", "--porcelain", "--", filepath.Base(file))
	if err != nil {
		return err
	}
	if strings.TrimSpace(dirty) != "" {
		return fmt.Errorf("%s has uncommitted changes, so nothing was written: commit or discard them first", file)
	}
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	before, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	after, done, err := edit(string(before))
	if err != nil {
		return err
	}
	if err := os.WriteFile(file, []byte(after), info.Mode().Perm()); err != nil {
		return err
	}
	if _, err := git(dir, "commit", "-q", "-m", "worklog "+msg, "--", filepath.Base(file)); err != nil {
		if werr := os.WriteFile(file, before, info.Mode().Perm()); werr != nil {
			return fmt.Errorf("commit failed (%w) and the log couldn't be put back: %w", err, werr)
		}
		return fmt.Errorf("commit failed, so the log was put back as it was: %w", err)
	}
	fmt.Fprintln(stdout, done)
	if stampFact(session) == "" {
		fmt.Fprintln(stderr, "worklog: no session ID in CLAUDE_CODE_SESSION_ID or CURSOR_CONVERSATION_ID, so the item carries no chat: stamp")
	}
	return nil
}

// git runs git in dir and returns its output.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(errb.String()+" "+err.Error()))
	}
	return out.String(), nil
}
