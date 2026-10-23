// Command aside is a side channel for thoughts while working with coding agents.
//
// Typing ">> some thought" in Claude Code or Codex stores the thought with
// git context and blocks the prompt, so the model never sees it.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/kacxx/aside-jot/internal/app"
	"github.com/kacxx/aside-jot/internal/hooks"
	"github.com/kacxx/aside-jot/internal/mcp"
	"github.com/mattn/go-isatty"
)

var version = "" // set with -ldflags "-X main.version=..."

// promoteRunner runs git and gh for aside promote; tests replace it.
var promoteRunner app.Runner = app.ExecRunner{}

// promptOut is where promote shows the issue and asks, so stdout carries only
// the URL (`url=$(aside promote 12 --yes)`, `aside promote 12 | pbcopy`).
// Tests replace it.
var promptOut io.Writer = os.Stderr

// stdinIsTerminal reports whether r is an interactive terminal. promote asks
// for confirmation only there, so a plain pipe ("echo y | aside promote 12")
// can't answer. A pseudo-terminal still can: a process that runs commands
// in one, or under script(1), can type "y". Tests replace it.
var stdinIsTerminal = func(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && (isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd()))
}

const usage = `aside — a side channel for thoughts while working with coding agents

Usage:
  aside add <text...>        capture a jot from the terminal (reads stdin if no text)
  aside inbox [-n N] [--older D]  list the newest inbox jots (default 20, 0 = all),
                             optionally only those at least D days old
  aside show <id>            show one jot with its context
  aside activity <id>        what a jot's own session did after it (Claude Code, Claude Desktop)
  aside open <id>            open the chat a jot came from (macOS, Windows; Claude Desktop, Codex app)
  aside search <query...>    search all jots (substring, case-insensitive)
  aside find <query...>      sessions with a jot matching the query, and how to resume them
  aside sessions [-n N]      recent sessions with their label (default 10, 0 = all)
  aside done <id>... [--note "why"]  mark jots as done, optionally recording why
  aside promote <id> [--repo owner/name] [--with-reply] [--yes [--public]] [--dry-run]
                             turn a jot into a GitHub issue with the gh CLI
  aside backup <path>        write a consistent copy of the database (never overwrites)
  aside paths                print data paths and check which aside is on PATH
  aside setup claude|codex [--dry-run] [--mcp]
                             add the prompt hook to the agent's config file
  aside hook claude|codex|cursor
                             run as a prompt hook (reads the payload on stdin)
  aside mcp                  run the read-only MCP server on stdio
  aside version

Environment:
  JOT_DB                     database path (default: $XDG_DATA_HOME/jot/jot.db,
                             else the OS per-user data directory)
  JOT_BUSY_TIMEOUT_MS        how long each database step waits on a lock
                             (default 2000, capped at 10000)
`

func main() {
	// If a reader closes its end, a write to stdout must fail with EPIPE rather
	// than kill the process with SIGPIPE: hooks exit 0 (or 2 for a Claude Code
	// or Codex block), and aside mcp and the CLI shut down through their normal
	// error paths.
	signal.Ignore(syscall.SIGPIPE)
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "hook" {
		os.Exit(runHook(args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	if err := run(args, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "aside:", err)
		os.Exit(1)
	}
}

// runHook returns 0, or 2 for a Claude Code or Codex block: a hook must never
// break the host editor.
func runHook(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(stderr, "aside hook: internal error:", r)
		}
	}()
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: aside hook claude|codex|cursor")
		return 0
	}
	var h func(context.Context, io.Reader, io.Writer, app.Opener) error
	switch args[0] {
	case "claude":
		h = hooks.Claude
	case "codex":
		h = hooks.Codex
	case "cursor":
		h = hooks.Cursor
	default:
		fmt.Fprintf(stderr, "aside hook: unknown agent %q (want claude, codex or cursor)\n", args[0])
		return 0
	}
	err := h(context.Background(), stdin, stdout, app.OpenDefault)
	var block *hooks.BlockExit
	if errors.As(err, &block) {
		fmt.Fprint(stderr, block.Reason)
		if block.WriteErr != nil {
			fmt.Fprintf(stderr, "\n\naside hook: %v", block.WriteErr)
		}
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "aside hook:", err)
	}
	return 0
}

func run(args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return nil
	}
	cmd, args := args[0], args[1:]
	ctx := context.Background()

	switch cmd {
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return nil
	case "version", "--version":
		fmt.Fprintln(stdout, "aside", buildVersion())
		return nil
	case "paths":
		return cmdPaths(stdout)
	case "setup":
		return cmdSetup(args, stdout)
	}

	svc, err := app.OpenDefault()
	if err != nil {
		return err
	}
	defer svc.Close()
	if desktopSessionsDir != "" {
		svc.SetDesktopSessionsDir(desktopSessionsDir)
	}

	switch cmd {
	case "add":
		text := strings.Join(args, " ")
		if strings.TrimSpace(text) == "" {
			b, err := io.ReadAll(stdin)
			if err != nil {
				return err
			}
			// Windows PowerShell can prepend a byte order mark to piped text.
			text = strings.TrimPrefix(string(b), "\ufeff")
		}
		cwd, _ := os.Getwd()
		e, err := svc.Capture(ctx, app.CaptureRequest{Text: text, Source: "cli", Cwd: cwd})
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "✓ Jotted #%d\n", e.ID)
	case "inbox":
		fs := flag.NewFlagSet("inbox", flag.ContinueOnError)
		n := fs.Int("n", 20, "number of jots to show (0 = all)")
		older := fs.Int("older", 0, "only jots at least N days old")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *older < 0 {
			return errors.New("--older must be 0 or more")
		}
		var es []app.Entry
		var err error
		if *older > 0 {
			es, err = svc.InboxOlder(ctx, *n, *older, now())
		} else {
			es, err = svc.Inbox(ctx, *n)
		}
		if err != nil {
			return err
		}
		if len(es) == 0 {
			if *older > 0 {
				fmt.Fprintf(stdout, "No inbox jots are %d or more days old.\n", *older)
			} else {
				fmt.Fprintln(stdout, "Inbox is empty.")
			}
		}
		svc.AddSessionTitles(es)
		printInbox(stdout, es)
	case "show":
		id, err := parseID(args)
		if err != nil {
			return err
		}
		e, err := svc.Show(ctx, id)
		if errors.Is(err, app.ErrNotFound) {
			return fmt.Errorf("no jot #%d", id)
		}
		if err != nil {
			return err
		}
		one := []app.Entry{e}
		svc.AddOpenURLs(ctx, one)
		printEntry(stdout, one[0])
	case "open":
		id, err := parseID(args)
		if err != nil {
			return err
		}
		return cmdOpen(ctx, svc, id, stdout)
	case "activity":
		id, err := parseID(args)
		if err != nil {
			return err
		}
		a, err := svc.Activity(ctx, id)
		if errors.Is(err, app.ErrNotFound) {
			return fmt.Errorf("no jot #%d", id)
		}
		if err != nil {
			return err
		}
		printActivity(stdout, a)
	case "search":
		q := strings.Join(args, " ")
		es, err := svc.Search(ctx, q, 0)
		if err != nil {
			return err
		}
		if len(es) == 0 {
			fmt.Fprintf(stdout, "No jots match %q.\n", q)
		}
		printList(stdout, es, q)
	case "find":
		q := strings.Join(args, " ")
		ss, loose, err := svc.Find(ctx, q)
		if err != nil {
			return err
		}
		svc.AddSessionOpenURLs(ss)
		if len(ss) == 0 && len(loose) == 0 {
			fmt.Fprintf(stdout, "No jots match %q.\n", q)
		}
		for i, s := range ss {
			if i > 0 {
				fmt.Fprintln(stdout)
			}
			printSession(stdout, s, s.Matches, q)
		}
		if len(loose) > 0 {
			if len(ss) > 0 {
				fmt.Fprintln(stdout)
			}
			fmt.Fprintln(stdout, "Not in a session:")
			printList(stdout, loose, q)
		}
	case "sessions":
		fs := flag.NewFlagSet("sessions", flag.ContinueOnError)
		n := fs.Int("n", 10, "number of sessions to show (0 = all)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		ss, err := svc.Sessions(ctx, *n)
		if err != nil {
			return err
		}
		svc.AddSessionOpenURLs(ss)
		if len(ss) == 0 {
			fmt.Fprintln(stdout, "No sessions yet: jots captured by a hook carry a session.")
		}
		for i, s := range ss {
			if i > 0 {
				fmt.Fprintln(stdout)
			}
			printSession(stdout, s, nil, "")
		}
	case "done":
		return cmdDone(ctx, svc, args, stdout)
	case "promote":
		return cmdPromote(ctx, svc, args, stdin, stdout)
	case "backup":
		if len(args) != 1 {
			return errors.New("usage: aside backup <path>")
		}
		if err := svc.Backup(ctx, args[0]); err != nil {
			return err
		}
		// Report the absolute path Backup wrote to, not the argument as typed.
		dst, err := filepath.Abs(args[0])
		if err != nil {
			dst = args[0]
		}
		fmt.Fprintln(stdout, "✓ Backed up to", dst)
	case "mcp":
		return mcp.NewServer(svc, buildVersion()).Serve(ctx, stdin, stdout)
	default:
		return fmt.Errorf("unknown command %q (run 'aside help')", cmd)
	}
	return nil
}

func cmdDone(ctx context.Context, svc *app.Service, args []string, w io.Writer) error {
	const usage = `usage: aside done <id>... [--note "why"]`
	fs := flag.NewFlagSet("done", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	note := fs.String("note", "", "why the jots are done; may contain links")
	// Flags may come before, between or after the ids.
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return fmt.Errorf("done: %w (%s)", err, usage)
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) == 0 {
		return errors.New("expected at least one jot id (" + usage + ")")
	}
	ids := make([]int64, len(pos))
	for i, p := range pos {
		id, err := parseID([]string{p})
		if err != nil {
			return err
		}
		ids[i] = id
	}
	closed, err := svc.DoneAll(ctx, ids, strings.TrimSpace(*note))
	if err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if closed[id] {
			fmt.Fprintf(w, "✓ #%d done\n", id)
		} else {
			fmt.Fprintf(w, "#%d already done\n", id)
		}
	}
	return nil
}

func cmdPromote(ctx context.Context, svc *app.Service, args []string, stdin io.Reader, w io.Writer) error {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	repo := fs.String("repo", "", "target repository, owner/name")
	dryRun := fs.Bool("dry-run", false, "print the issue without creating it")
	withReply := fs.Bool("with-reply", false, "include the agent's last reply before the jot (Claude Code only)")
	yes := fs.Bool("yes", false, "create the issue without asking first (required when stdin is not a terminal)")
	public := fs.Bool("public", false, "with --yes, create the issue even if the repo is public or its visibility can't be checked")
	// Flags may come before or after the id.
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return fmt.Errorf("promote: %w (usage: aside promote <id> [--repo owner/name] [--with-reply] [--yes [--public]] [--dry-run])", err)
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	id, err := parseID(pos)
	if err != nil {
		return fmt.Errorf("%w (usage: aside promote <id> [--repo owner/name] [--with-reply] [--yes [--public]] [--dry-run])", err)
	}
	req := app.PromoteRequest{ID: id, Repo: *repo, DryRun: *dryRun, WithReply: *withReply, Public: *public}
	if !*yes && !*dryRun {
		// A jot is a private note and the target repo may be public, so always
		// show what would be posted and ask. The terminal check is inside
		// Confirm, after Promote has found the jot and its repo, so a bad id
		// or an already-promoted jot gets its own error. Without a terminal
		// there is no one to ask, and an answer read from a pipe isn't one.
		req.Confirm = func(p app.Promotion) error {
			if !stdinIsTerminal(stdin) {
				return errors.New("promote posts to GitHub and asks first, but stdin is not a terminal; run it in a terminal, or pass --yes to skip the question")
			}
			fmt.Fprintf(promptOut, "repo:  %s\ntitle: %s\n\n%s\n%sCreate this issue? [y/N] ", p.Repo, p.Title, p.Body, warningLine(p))
			answer, _ := bufio.NewReader(stdin).ReadString('\n')
			if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
				return app.ErrNotConfirmed
			}
			return nil
		}
	}
	p, err := svc.Promote(ctx, promoteRunner, req)
	if errors.Is(err, app.ErrNotFound) {
		return fmt.Errorf("no jot #%d", id)
	}
	if errors.Is(err, app.ErrNotConfirmed) {
		return errors.New("not created (pass --yes to skip this question)")
	}
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Fprintf(w, "repo:  %s\ntitle: %s\n\n%s", p.Repo, p.Title, p.Body)
		if line := warningLine(p); line != "" {
			fmt.Fprintf(w, "\n%s", line)
		}
		return nil
	}
	fmt.Fprintln(w, p.URL)
	return nil
}

// warningLine is p's public-repo warning as a line of its own, or "".
func warningLine(p app.Promotion) string {
	if p.Warning == "" {
		return ""
	}
	return p.Warning + "\n"
}

// desktopSessionsDir, if set, replaces where Claude Desktop's session files
// are looked for. Tests set it.
var desktopSessionsDir string

// openGOOS and openURL are what aside open runs on; tests replace them.
var (
	openGOOS = runtime.GOOS
	openURL  = func(ctx context.Context, u string) error {
		bin, args := openCommand(openGOOS, u)
		return openWith(ctx, bin, args...)
	}
)

// openCommand is the program and arguments that hand u to the system's
// default handler for its scheme. u has already been checked by
// app.OpenableURL, so it holds only a scheme, a UUID and a few fixed
// characters. On Windows it goes to rundll32 so no shell parses it; rundll32
// reports no failure, so a link nothing handles prints "opened" there anyway.
func openCommand(goos, u string) (string, []string) {
	if goos == "windows" {
		return "rundll32", []string{"url.dll,FileProtocolHandler", u}
	}
	return "/usr/bin/open", []string{u}
}

// openWith runs bin with args, and keeps what it printed if it fails: open
// says why, for example that no application handles the link's scheme.
func openWith(ctx context.Context, bin string, args ...string) error {
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
	}
	return err
}

// cmdOpen opens the chat a jot came from. The link is rebuilt the way show
// builds it, and only the two known kinds are passed to the system, so a
// stored value can't make aside open anything else.
func cmdOpen(ctx context.Context, svc *app.Service, id int64, w io.Writer) error {
	e, err := svc.Show(ctx, id)
	if errors.Is(err, app.ErrNotFound) {
		return fmt.Errorf("no jot #%d", id)
	}
	if err != nil {
		return err
	}
	one := []app.Entry{e}
	svc.AddOpenURLs(ctx, one)
	u := one[0].OpenURL
	if u == "" || !app.OpenableURL(u) {
		msg := fmt.Sprintf("jot #%d has no link to its chat", id)
		if cmd := app.ResumeFor(e); cmd != "" {
			msg += "; to reopen it, run: " + cmd
		}
		return errors.New(msg)
	}
	if !app.CanOpen(openGOOS) {
		return fmt.Errorf("aside open is only supported on macOS and Windows; the link is %s", u)
	}
	if err := openURL(ctx, u); err != nil {
		return fmt.Errorf("opening %s: %w", u, err)
	}
	fmt.Fprintln(w, "opened", u)
	return nil
}

func cmdPaths(w io.Writer) error {
	db, err := app.DBPath()
	if err != nil {
		return err
	}
	src := "default"
	switch {
	case os.Getenv("JOT_DB") != "":
		src = "$JOT_DB"
	case os.Getenv("XDG_DATA_HOME") != "":
		src = "$XDG_DATA_HOME"
	}
	fmt.Fprintf(w, "db:           %s (%s)\n", db, src)
	fmt.Fprintf(w, "busy timeout: %s\n", app.BusyTimeout())
	exe, err := os.Executable()
	if err != nil {
		// Still exit 0: the data paths above are the main output.
		fmt.Fprintf(w, "binary:       unknown (%v)\n", err)
		fmt.Fprintln(w, "warning:      could not check which 'aside' is on PATH")
		return nil
	}
	fmt.Fprintf(w, "binary:       %s\n", exe)
	if warn := pathWarning(exe, exec.LookPath); warn != "" {
		fmt.Fprintln(w, "warning:     ", warn)
	}
	return nil
}

// pathWarning reports when a bare "aside" would not run exe: nothing named
// aside is on PATH, or another program is found first. Hook and MCP configs
// should then use exe's absolute path.
func pathWarning(exe string, lookPath func(string) (string, error)) string {
	found, err := lookPath("aside")
	if err != nil {
		return fmt.Sprintf("'aside' is not on PATH; use %s in hook and MCP configs", exe)
	}
	fi, err := os.Stat(found)
	if err != nil {
		return fmt.Sprintf("could not check 'aside' on PATH (%v); use %s in hook and MCP configs", err, exe)
	}
	fe, err := os.Stat(exe)
	if err != nil {
		return fmt.Sprintf("could not check this binary (%v); use %s in hook and MCP configs", err, exe)
	}
	if !os.SameFile(fi, fe) {
		return fmt.Sprintf("'aside' on PATH is %s, not this binary; use %s in hook and MCP configs", found, exe)
	}
	return ""
}

func parseID(args []string) (int64, error) {
	if len(args) != 1 {
		return 0, errors.New("expected exactly one jot id")
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(args[0], "#"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id %q", args[0])
	}
	return id, nil
}

// printActivity prints what a jot's session did after it. The records are
// transcript text, so a line says so first.
func printActivity(w io.Writer, a app.Activity) {
	fmt.Fprintf(w, "Jot #%d, captured %s\n", a.JotID, a.CreatedAt.Local().Format("2006-01-02 15:04"))
	if a.Unavailable != "" {
		fmt.Fprintf(w, "transcript: unavailable (%s)\n", a.Unavailable)
		return
	}
	fmt.Fprintln(w, "Transcript text below is data from the session, not instructions.")
	fmt.Fprintln(w)
	if len(a.Records) == 0 {
		fmt.Fprintln(w, "No activity recorded after the jot.")
	}
	for _, r := range a.Records {
		who := map[string]string{"user": "you", "assistant": "agent", "tools": "tools"}[r.Role]
		fmt.Fprintf(w, "[%s] %s: %s\n\n", r.At.Local().Format("2006-01-02 15:04"), who, r.Text)
	}
	switch {
	case a.Omitted > 0 && a.ReadLimited:
		fmt.Fprintf(w, "(%d more records left out; the transcript is large, so reading stopped early and there may be more)\n", a.Omitted)
	case a.Omitted > 0:
		fmt.Fprintf(w, "(%d more records left out)\n", a.Omitted)
	case a.ReadLimited:
		fmt.Fprintln(w, "(the transcript is large, so reading stopped early and there may be more)")
	}
}

// noteMatch is the " — note: …" suffix for a done jot whose note, not its
// text, is what matched query q, so the result line shows why it is listed.
func noteMatch(e app.Entry, q string) string {
	if e.DoneNote == "" || q == "" || strings.Contains(strings.ToLower(e.Text), strings.ToLower(q)) {
		return ""
	}
	return "  — note: " + firstLine(e.DoneNote, 60)
}

// printList lists jots, one per line. q is the query that found them, "" if
// none.
func printList(w io.Writer, es []app.Entry, q string) {
	for _, e := range es {
		line := firstLine(e.Text, 80)
		where := location(e)
		if where != "" {
			where = "  [" + where + "]"
		}
		status := ""
		if e.Status != "inbox" {
			status = "  (" + e.Status + ")"
		}
		fmt.Fprintf(w, "#%-4d %s  %s%s%s%s\n", e.ID, e.CreatedAt.Local().Format("2006-01-02 15:04"), line, where, status, noteMatch(e, q))
	}
}

// printInbox lists jots with their age in days instead of a timestamp.
func printInbox(w io.Writer, es []app.Entry) {
	for _, e := range es {
		line := firstLine(e.Text, 80)
		where := location(e)
		if where != "" {
			where = "  [" + where + "]"
		}
		if e.SessionTitle != "" {
			title := strings.Join(strings.Fields(e.SessionTitle), " ")
			if where == "" {
				where = "  · " + title
			} else {
				where += " · " + title
			}
		}
		fmt.Fprintf(w, "#%-4d %-6s %s%s\n", e.ID, age(e.CreatedAt, now()), line, where)
	}
}

// printSession prints a session's label, a summary line, the given jots, the
// command to resume it and, if the chat has a link, the command to open it.
func printSession(w io.Writer, s app.Session, jots []app.Entry, q string) {
	fmt.Fprintln(w, firstLine(s.LabelText(), 80))
	latest := s.Latest()
	parts := []string{s.Source}
	if where := location(latest); where != "" {
		parts = append(parts, where)
	}
	count := fmt.Sprintf("%d jots", len(s.Jots))
	if len(s.Jots) == 1 {
		count = "1 jot"
	}
	parts = append(parts, count, fmt.Sprintf("last %s (#%d)", age(latest.CreatedAt, now()), latest.ID))
	fmt.Fprintln(w, "  "+strings.Join(parts, " · "))
	for _, e := range jots {
		status := ""
		if e.Status != "inbox" {
			status = "  (" + e.Status + ")"
		}
		fmt.Fprintf(w, "  #%-4d %-6s %s%s%s\n", e.ID, age(e.CreatedAt, now()), firstLine(e.Text, 70), status, noteMatch(e, q))
	}
	if cmd := s.ResumeCommand(); cmd != "" {
		fmt.Fprintln(w, "  "+cmd)
	}
	if app.OpenableURL(s.OpenURL) {
		fmt.Fprintf(w, "  aside open %d\n", latest.ID)
	}
}

// firstLine returns text's first line, cut to max runes with an ellipsis.
func firstLine(text string, max int) string {
	line, _, _ := strings.Cut(text, "\n")
	if r := []rune(line); len(r) > max {
		line = string(r[:max-1]) + "…"
	}
	return line
}

// now is the clock for jot ages; tests replace it.
var now = time.Now

// age is how many calendar days ago t was in local time: "today", "1d", "12d".
func age(t, ref time.Time) string {
	days := app.AgeDays(t, ref)
	if days == 0 {
		return "today"
	}
	return strconv.Itoa(days) + "d"
}

func location(e app.Entry) string {
	switch {
	case e.RepoName != "" && e.Branch != "":
		return e.RepoName + "@" + e.Branch
	case e.RepoName != "" && e.CommitSHA != "":
		return e.RepoName + "@" + e.CommitSHA
	default:
		return e.RepoName
	}
}

func printEntry(w io.Writer, e app.Entry) {
	fmt.Fprintf(w, "#%d  (%s)\n\n%s\n\n", e.ID, e.Status, e.Text)
	field := func(k, v string) {
		if v != "" {
			fmt.Fprintf(w, "%-10s %s\n", k+":", v)
		}
	}
	field("created", e.CreatedAt.Local().Format(time.RFC3339))
	field("source", e.Source)
	field("session", e.SessionID)
	field("cwd", e.Cwd)
	field("repo", e.RepoName)
	field("root", e.RepoRoot)
	field("branch", e.Branch)
	field("commit", e.CommitSHA)
	field("issue", e.IssueURL)
	field("note", e.DoneNote)
	field("open", e.OpenURL)
	if len(e.Metadata) > 0 {
		field("metadata", string(e.Metadata))
	}
}

func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "dev"
}
