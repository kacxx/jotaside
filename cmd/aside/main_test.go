package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kacxx/aside-jot/internal/app"
)

func TestAddStdinBOM(t *testing.T) {
	t.Setenv("JOT_DB", filepath.Join(t.TempDir(), "jot.db"))
	add := func(stdin string, args ...string) error {
		return run(append([]string{"add"}, args...), strings.NewReader(stdin), &bytes.Buffer{})
	}
	show := func() string {
		var out bytes.Buffer
		if err := run([]string{"show", "1"}, nil, &out); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}

	if err := add("\ufeffbom jot\r\n"); err != nil {
		t.Fatal(err)
	}
	got := show()
	if !strings.Contains(got, "bom jot") || strings.Contains(got, "\ufeff") {
		t.Fatalf("stdin BOM stored: %q", got)
	}

	for _, in := range []string{"\ufeff", "\ufeff\r\n", ""} {
		if err := add(in); err == nil || !strings.Contains(err.Error(), "empty jot") {
			t.Fatalf("stdin %q: want empty jot, got %v", in, err)
		}
	}

	// Only one BOM is stripped; the rest of the text is kept as is.
	if err := add("\ufeff\ufeffx"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"show", "2"}, nil, &out); err != nil || !strings.Contains(out.String(), "\ufeffx") {
		t.Fatalf("second BOM: %q %v", out.String(), err)
	}

	// Text given as arguments is unchanged.
	if err := add("", "\ufeffarg"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"show", "3"}, nil, &out); err != nil || !strings.Contains(out.String(), "\ufeffarg") {
		t.Fatalf("arg BOM: %q %v", out.String(), err)
	}
}

func TestCLIFlow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JOT_DB", filepath.Join(dir, "jot.db"))
	sh := func(stdin string, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := run(args, strings.NewReader(stdin), &out); err != nil {
			t.Fatalf("aside %v: %v", args, err)
		}
		return out.String()
	}

	if got := sh("", "add", "remember", "the", "milk"); got != "✓ Jotted #1\n" {
		t.Fatalf("add: %q", got)
	}
	if got := sh("from stdin\n", "add"); got != "✓ Jotted #2\n" {
		t.Fatalf("add stdin: %q", got)
	}
	if got := sh("", "inbox", "-n", "1"); !strings.Contains(got, "#2") || strings.Contains(got, "#1 ") {
		t.Fatalf("inbox -n 1: %q", got)
	}
	if got := sh("", "show", "#1"); !strings.Contains(got, "remember the milk") || !strings.Contains(got, "source:    cli") {
		t.Fatalf("show: %q", got)
	}
	if got := sh("", "search", "MILK"); !strings.Contains(got, "#1") {
		t.Fatalf("search: %q", got)
	}
	sh("", "done", "1")
	if got := sh("", "inbox"); strings.Contains(got, "milk") {
		t.Fatalf("inbox after done: %q", got)
	}
	bk := filepath.Join(dir, "b.db")
	sh("", "backup", bk)
	if err := run([]string{"backup", bk}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("backup must refuse to overwrite")
	}
	if err := run([]string{"show", "99"}, nil, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "no jot #99") {
		t.Fatalf("show missing: %v", err)
	}
	if got := sh("", "paths"); !strings.Contains(got, "jot.db ($JOT_DB)") {
		t.Fatalf("paths: %q", got)
	}
}

// resumeAPI is the resume command printed for the session in /work/api; on
// Windows it is written for PowerShell.
func init() {
	resumeAPI = "cd /work/api && claude --resume c1"
	if runtime.GOOS == "windows" {
		resumeAPI = `Set-Location -LiteralPath "/work/api"; claude --resume c1`
	}
}

var resumeAPI string

func TestFindAndSessions(t *testing.T) {
	db := filepath.Join(t.TempDir(), "jot.db")
	t.Setenv("JOT_DB", db)
	svc, err := app.Open(db, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, r := range []app.CaptureRequest{
		{Text: "session: SUP-4821 token TTL", Source: "claude", SessionID: "c1", Cwd: "/work/api"},
		{Text: "SUP-4821 needs a backend ticket", Source: "claude", SessionID: "c1", Cwd: "/work/api"},
		{Text: "SUP-4821 from the terminal", Source: "cli"},
	} {
		if _, err := svc.Capture(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Done(ctx, 2); err != nil {
		t.Fatal(err)
	}
	svc.Close()
	old := now
	now = func() time.Time { return time.Now().AddDate(0, 0, 3) }
	t.Cleanup(func() { now = old })

	sh := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := run(args, strings.NewReader(""), &out); err != nil {
			t.Fatalf("aside %v: %v", args, err)
		}
		return out.String()
	}
	got := sh("find", "sup-4821")
	for _, want := range []string{
		"SUP-4821 token TTL\n",
		"  claude · 2 jots · last 3d (#2)\n",
		"  #1    3d     session: SUP-4821 token TTL\n",
		"  #2    3d     SUP-4821 needs a backend ticket  (done)\n",
		"  " + resumeAPI + "\n",
		"Not in a session:\n#3 ",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("find output missing %q:\n%s", want, got)
		}
	}
	if got := sh("find", "nope"); got != "No jots match \"nope\".\n" {
		t.Fatalf("find no match: %q", got)
	}
	got = sh("sessions")
	if !strings.HasPrefix(got, "SUP-4821 token TTL\n  claude · 2 jots · last 3d (#2)\n  "+resumeAPI+"\n") {
		t.Fatalf("sessions: %q", got)
	}
}

func TestAge(t *testing.T) {
	ref := time.Date(2026, 10, 2, 9, 0, 0, 0, time.Local)
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{ref.Add(-time.Hour), "today"},
		{time.Date(2026, 10, 1, 23, 50, 0, 0, time.Local), "1d"},
		{ref.AddDate(0, 0, -12), "12d"},
		{ref.Add(time.Hour), "today"},
	} {
		if got := age(c.t, ref); got != c.want {
			t.Errorf("age(%v): got %q, want %q", c.t, got, c.want)
		}
	}
}

// A Claude Code or Codex block exits 2 with the reason on stderr, so it still
// blocks if the host rejects the JSON. Everything else exits 0.
func TestHookExitCodes(t *testing.T) {
	t.Setenv("JOT_DB", filepath.Join(t.TempDir(), "file", "jot.db"))
	// A regular file where the data directory should be makes the DB unopenable.
	if err := os.WriteFile(filepath.Dir(os.Getenv("JOT_DB")), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tooLarge := `{"pad":"` + strings.Repeat("x", 17<<20) + `","prompt":">> x"}`

	for _, c := range []struct {
		args   []string
		prompt string
		code   int
	}{
		{[]string{}, `{"prompt":">> x"}`, 0},
		{[]string{"nope"}, `{"prompt":">> x"}`, 0},
		{[]string{"claude"}, `{"prompt":">> x"}`, 2},
		{[]string{"claude"}, tooLarge, 2},
		{[]string{"claude"}, `{"prompt":">> x","hook_event_name":"beforeSubmitPrompt"}`, 0},
		{[]string{"claude"}, `{"prompt":"hello"}`, 0},
		{[]string{"codex"}, `{"prompt":">> x"}`, 2},
		{[]string{"codex"}, `{"prompt":"hello"}`, 0},
		{[]string{"cursor"}, `{"prompt":">> x"}`, 0},
	} {
		var out, errb bytes.Buffer
		name := c.prompt
		if len(name) > 60 {
			name = name[:60] + "…"
		}
		if code := runHook(c.args, strings.NewReader(c.prompt), &out, &errb); code != c.code {
			t.Errorf("hook %v %s exited %d, want %d", c.args, name, code, c.code)
		}
		jot := strings.Contains(c.prompt, ">>")
		if len(c.args) == 1 && c.args[0] != "nope" && jot && !strings.Contains(out.String(), "NOT saved") {
			t.Errorf("hook %v %s: stdout %q", c.args, name, out.String())
		}
		if c.code == 2 && (!strings.HasPrefix(errb.String(), "✗ Jot NOT saved") || !strings.Contains(errb.String(), ">> x")) {
			t.Errorf("hook %v %s: stderr %q, want the reason", c.args, name, errb.String())
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("stdout closed") }

// A Claude Code block that can't write its JSON still exits 2, and says why
// after the reason.
func TestHookBlockWriteError(t *testing.T) {
	t.Setenv("JOT_DB", filepath.Join(t.TempDir(), "jot.db"))
	var errb bytes.Buffer
	if code := runHook([]string{"claude"}, strings.NewReader(`{"prompt":">> x"}`), failWriter{}, &errb); code != 2 {
		t.Fatalf("exited %d, want 2", code)
	}
	if got := errb.String(); !strings.HasPrefix(got, "✓ Jotted #1") || !strings.HasSuffix(got, "aside hook: stdout closed") {
		t.Fatalf("stderr %q", got)
	}
}

// The backup message reports the absolute path written, not the argument.
func TestBackupReportsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("JOT_DB", filepath.Join(dir, "jot.db"))
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	var out bytes.Buffer
	if err := run([]string{"backup", filepath.Join("..", "b.db")}, nil, &out); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "b.db")
	if got := strings.TrimSpace(out.String()); got != "✓ Backed up to "+want {
		t.Fatalf("got %q, want path %q", got, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

// paths warns when a bare "aside" would not run this binary, as on macOS
// where an unrelated tool could shadow it (issue #6).
func TestPathWarning(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "self")
	other := filepath.Join(dir, "other")
	for _, p := range []string{self, other} {
		if err := os.WriteFile(p, nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	look := func(found string, err error) func(string) (string, error) {
		return func(name string) (string, error) {
			if name != "aside" {
				t.Errorf("looked up %q", name)
			}
			return found, err
		}
	}

	if got := pathWarning(self, look(self, nil)); got != "" {
		t.Errorf("same binary: %q", got)
	}
	if got := pathWarning(self, look(other, nil)); !strings.Contains(got, other) || !strings.Contains(got, "use "+self) {
		t.Errorf("shadowed: %q", got)
	}
	if got := pathWarning(self, look("", exec.ErrNotFound)); !strings.Contains(got, "not on PATH") || !strings.Contains(got, self) {
		t.Errorf("missing: %q", got)
	}
	// A file that can't be stat'ed is reported as unchecked, not as a
	// different binary.
	gone := filepath.Join(dir, "gone")
	if got := pathWarning(self, look(gone, nil)); !strings.Contains(got, "could not check 'aside' on PATH") || !strings.Contains(got, "use "+self) {
		t.Errorf("PATH entry vanished: %q", got)
	}
	if got := pathWarning(gone, look(self, nil)); !strings.Contains(got, "could not check this binary") || !strings.Contains(got, "use "+gone) {
		t.Errorf("binary vanished: %q", got)
	}
}

// ghFake stands in for git and gh: no real GitHub calls.
type ghFake struct{ calls []string }

func (f *ghFake) Run(_ context.Context, _ io.Reader, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name == "gh" && args[0] == "issue" {
		return []byte("https://github.com/o/n/issues/7\n"), nil
	}
	return nil, nil
}

// setTerminal makes promote treat stdin as a terminal (or not) for the test.
func setTerminal(t *testing.T, isTerminal bool) {
	t.Helper()
	old := stdinIsTerminal
	stdinIsTerminal = func(io.Reader) bool { return isTerminal }
	t.Cleanup(func() { stdinIsTerminal = old })
}

// capturePrompt collects what promote writes to promptOut.
func capturePrompt(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := promptOut
	promptOut = &buf
	t.Cleanup(func() { promptOut = old })
	return &buf
}

func TestStdinIsTerminalReal(t *testing.T) {
	if stdinIsTerminal(strings.NewReader("y\n")) {
		t.Error("a strings.Reader is not a terminal")
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	if stdinIsTerminal(pr) {
		t.Error("a pipe is not a terminal")
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if stdinIsTerminal(null) {
		t.Error("/dev/null is not a terminal")
	}
}

func TestPromoteCLI(t *testing.T) {
	t.Setenv("JOT_DB", filepath.Join(t.TempDir(), "jot.db"))
	setTerminal(t, true)
	prompt := capturePrompt(t)
	f := &ghFake{}
	old := promoteRunner
	promoteRunner = f
	t.Cleanup(func() { promoteRunner = old })
	aside := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := run(args, strings.NewReader("y\n"), &out)
		return out.String(), err
	}

	if _, err := aside("add", "ship it\nwith details"); err != nil {
		t.Fatal(err)
	}
	out, err := aside("promote", "1", "--dry-run", "--repo", "o/n")
	if err != nil || !strings.HasPrefix(out, "repo:  o/n\ntitle: ship it\n\nship it\nwith details\n\nCaptured ") {
		t.Fatalf("dry run: %q, %v", out, err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("dry run ran %v", f.calls)
	}
	if out, _ := aside("show", "1"); strings.Contains(out, "issue:") || !strings.Contains(out, "(inbox)") {
		t.Fatalf("dry run changed the jot: %q", out)
	}

	// The jot was added outside a repo, so the target must be given.
	if _, err := aside("promote", "1"); err == nil || !strings.Contains(err.Error(), "pass --repo") {
		t.Fatalf("no repo: %v", err)
	}
	if out, err := aside("promote", "--repo=o/n", "#1"); err != nil || out != "https://github.com/o/n/issues/7\n" {
		t.Fatalf("promote: %q, %v", out, err)
	}
	if !strings.Contains(prompt.String(), "title: ship it") || !strings.HasSuffix(prompt.String(), "[y/N] ") {
		t.Fatalf("prompt: %q", prompt.String())
	}
	if out, _ := aside("show", "1"); !strings.Contains(out, "(done)") || !strings.Contains(out, "issue:     https://github.com/o/n/issues/7") {
		t.Fatalf("show after promote: %q", out)
	}
	if _, err := aside("promote", "1", "--repo", "o/n"); err == nil || !strings.Contains(err.Error(), "already promoted to https://github.com/o/n/issues/7") {
		t.Fatalf("second promote: %v", err)
	}
	if _, err := aside("promote", "99", "--repo", "o/n"); err == nil || !strings.Contains(err.Error(), "no jot #99") {
		t.Fatalf("unknown id: %v", err)
	}
	for _, args := range [][]string{{"promote"}, {"promote", "1", "2"}, {"promote", "1", "--bogus"}, {"promote", "1", "--repo"}} {
		if _, err := aside(args...); err == nil || !strings.Contains(err.Error(), "usage: aside promote") {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestInboxShowsAge(t *testing.T) {
	t.Setenv("JOT_DB", filepath.Join(t.TempDir(), "jot.db"))
	ctx := context.Background()
	svc, err := app.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Capture(ctx, app.CaptureRequest{Text: "fresh thought", Source: "cli"}); err != nil {
		t.Fatal(err)
	}
	svc.Close()
	old := now
	t.Cleanup(func() { now = old })
	now = time.Now
	sh := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := run(args, strings.NewReader(""), &out); err != nil {
			t.Fatalf("aside %v: %v", args, err)
		}
		return out.String()
	}
	got := sh("inbox")
	if !strings.Contains(got, "today  fresh thought") || strings.Contains(got, "20") {
		t.Fatalf("inbox today: %q", got)
	}
	now = func() time.Time { return time.Now().AddDate(0, 0, 3) }
	if got := sh("inbox"); !strings.Contains(got, "#1    3d     fresh thought") {
		t.Fatalf("inbox 3d: %q", got)
	}
	if got := sh("inbox", "--older", "3"); !strings.Contains(got, "fresh thought") {
		t.Fatalf("--older 3: %q", got)
	}
	if got := sh("inbox", "--older", "4"); got != "No inbox jots are 4 or more days old.\n" {
		t.Fatalf("--older 4: %q", got)
	}
	if got := sh("show", "1"); !strings.Contains(got, "created:") {
		t.Fatalf("show keeps the timestamp: %q", got)
	}
}

func TestPromoteWithReplyCLI(t *testing.T) {
	ctx := context.Background()
	t.Setenv("JOT_DB", filepath.Join(t.TempDir(), "jot.db"))
	setTerminal(t, true)
	prompt := capturePrompt(t)
	f := &ghFake{}
	old := promoteRunner
	promoteRunner = f
	t.Cleanup(func() { promoteRunner = old })

	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	rec := `{"type":"user","timestamp":"2020-01-01T00:00:00Z","message":{"role":"user","content":"hi"}}
{"type":"assistant","timestamp":"2020-01-01T00:00:01Z","message":{"id":"m","role":"assistant","content":[{"type":"text","text":"the diagnosis"}]}}
`
	if err := os.WriteFile(transcript, []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := app.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"note", "second note"} {
		if _, err := svc.Capture(ctx, app.CaptureRequest{Text: text, Source: "claude",
			Metadata: map[string]any{"transcript_path": transcript}}); err != nil {
			t.Fatal(err)
		}
	}
	svc.Close()
	issues := func() int {
		n := 0
		for _, c := range f.calls {
			if strings.HasPrefix(c, "gh issue") {
				n++
			}
		}
		return n
	}

	aside := func(stdin string, args ...string) (string, error) {
		var out bytes.Buffer
		err := run(args, strings.NewReader(stdin), &out)
		return out.String(), err
	}
	// Declined (or no answer): the preview is shown and nothing is created.
	for _, answer := range []string{"", "n\n"} {
		prompt.Reset()
		out, err := aside(answer, "promote", "1", "--repo", "o/n", "--with-reply")
		if err == nil || out != "" || !strings.Contains(prompt.String(), "the diagnosis") || !strings.Contains(prompt.String(), "[y/N]") {
			t.Fatalf("declined %q: %q, %q, %v", answer, out, prompt.String(), err)
		}
	}
	if issues() != 0 {
		t.Fatalf("declined promote ran %v", f.calls)
	}
	// A dry run prints the body without asking.
	prompt.Reset()
	if out, err := aside("", "promote", "1", "--repo", "o/n", "--with-reply", "--dry-run"); err != nil ||
		!strings.Contains(out, "the diagnosis") || prompt.Len() != 0 || issues() != 0 {
		t.Fatalf("dry run: %q, %v", out, err)
	}
	// Confirmed.
	if out, err := aside("y\n", "promote", "1", "--repo", "o/n", "--with-reply"); err != nil ||
		out != "https://github.com/o/n/issues/7\n" || issues() != 1 {
		t.Fatalf("confirmed: %q, %v", out, err)
	}
	// --yes creates without asking or reading stdin.
	prompt.Reset()
	if out, err := aside("", "promote", "2", "--repo", "o/n", "--with-reply", "--yes"); err != nil ||
		prompt.Len() != 0 || issues() != 2 {
		t.Fatalf("--yes: %q, %v", out, err)
	}
}

func TestPromoteConfirmCLI(t *testing.T) {
	t.Setenv("JOT_DB", filepath.Join(t.TempDir(), "jot.db"))
	prompt := capturePrompt(t)
	f := &ghFake{}
	old := promoteRunner
	promoteRunner = f
	t.Cleanup(func() { promoteRunner = old })
	aside := func(stdin string, args ...string) (string, error) {
		var out bytes.Buffer
		err := run(args, strings.NewReader(stdin), &out)
		return out.String(), err
	}
	issues := func() int {
		n := 0
		for _, c := range f.calls {
			if strings.HasPrefix(c, "gh issue") {
				n++
			}
		}
		return n
	}
	for _, text := range []string{"one", "two", "three", "four"} {
		if _, err := aside("", "add", text); err != nil {
			t.Fatal(err)
		}
	}

	// No terminal and no --yes: refused, even with "y" piped in, and nothing
	// is asked or created.
	setTerminal(t, false)
	if out, err := aside("y\n", "promote", "1", "--repo", "o/n"); err == nil ||
		!strings.Contains(err.Error(), "not a terminal") || out != "" || prompt.Len() != 0 || issues() != 0 {
		t.Fatalf("no terminal: %q, %v, %v", out, err, f.calls)
	}
	// A bad id still gets its own error, not the terminal one.
	if _, err := aside("y\n", "promote", "99", "--repo", "o/n"); err == nil || !strings.Contains(err.Error(), "no jot #99") {
		t.Fatalf("unknown id without a terminal: %v", err)
	}
	// A dry run needs no answer, so it works without a terminal.
	if out, err := aside("", "promote", "1", "--repo", "o/n", "--dry-run"); err != nil ||
		!strings.Contains(out, "title: one") || prompt.Len() != 0 || issues() != 0 {
		t.Fatalf("dry run: %q, %v", out, err)
	}
	// --yes works without a terminal and does not ask.
	if out, err := aside("", "promote", "2", "--repo", "o/n", "--yes"); err != nil ||
		out != "https://github.com/o/n/issues/7\n" || prompt.Len() != 0 || issues() != 1 {
		t.Fatalf("--yes: %q, %v", out, err)
	}
	// An already-promoted jot reports its URL, not the terminal error.
	if _, err := aside("y\n", "promote", "2", "--repo", "o/n"); err == nil || !strings.Contains(err.Error(), "already promoted to") {
		t.Fatalf("already promoted without a terminal: %v", err)
	}

	// With a terminal, plain promote shows the issue on stderr and asks.
	setTerminal(t, true)
	for _, answer := range []string{"", "n\n"} {
		prompt.Reset()
		out, err := aside(answer, "promote", "1", "--repo", "o/n")
		if err == nil || out != "" || !strings.Contains(prompt.String(), "repo:  o/n\ntitle: one") || !strings.HasSuffix(prompt.String(), "[y/N] ") {
			t.Fatalf("declined %q: %q, %q, %v", answer, out, prompt.String(), err)
		}
	}
	if issues() != 1 {
		t.Fatalf("declined promote ran %v", f.calls)
	}
	if out, err := aside("y\n", "promote", "1", "--repo", "o/n"); err != nil ||
		out != "https://github.com/o/n/issues/7\n" || issues() != 2 {
		t.Fatalf("confirmed: %q, %v", out, err)
	}
	// --yes skips the question on a terminal too.
	prompt.Reset()
	if out, err := aside("", "promote", "3", "--repo", "o/n", "--yes"); err != nil ||
		out != "https://github.com/o/n/issues/7\n" || prompt.Len() != 0 || issues() != 3 {
		t.Fatalf("--yes on a terminal: %q, %v", out, err)
	}
}

func TestCLIDoneSeveralWithNote(t *testing.T) {
	t.Setenv("JOT_DB", filepath.Join(t.TempDir(), "jot.db"))
	sh := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := run(args, strings.NewReader(""), &out)
		return out.String(), err
	}
	for _, txt := range []string{"one", "two", "three", "four"} {
		if _, err := sh("add", txt); err != nil {
			t.Fatal(err)
		}
	}
	// A missing id fails naming it and closes nothing.
	if _, err := sh("done", "1", "99", "98"); err == nil || !strings.Contains(err.Error(), "no jots #99, #98") {
		t.Fatalf("done with missing ids: %v", err)
	}
	if got, _ := sh("inbox"); !strings.Contains(got, "one") {
		t.Fatalf("nothing should be closed: %q", got)
	}
	if got, err := sh("done", "1", "#2", "--note", "see https://example.com/wiki/x"); err != nil || got != "✓ #1 done\n✓ #2 done\n" {
		t.Fatalf("done: %q %v", got, err)
	}
	if got, _ := sh("show", "2"); !strings.Contains(got, "note:      see https://example.com/wiki/x") || !strings.Contains(got, "(done)") {
		t.Fatalf("show: %q", got)
	}
	if got, _ := sh("search", "WIKI/x"); !strings.Contains(got, "#1") || !strings.Contains(got, "#2") || strings.Contains(got, "#3") {
		t.Fatalf("search by note: %q", got)
	}
	if got, _ := sh("search", "wiki"); !strings.Contains(got, "— note: see https://example.com/wiki/x") {
		t.Fatalf("search shows the note that matched: %q", got)
	}
	if got, _ := sh("search", "one"); strings.Contains(got, "note:") {
		t.Fatalf("no note suffix when the text matched: %q", got)
	}
	if got, err := sh("done", "1"); err != nil || got != "#1 already done\n" {
		t.Fatalf("done again: %q %v", got, err)
	}
	if got, _ := sh("find", "wiki"); strings.Contains(got, "No jots match") {
		t.Fatalf("find by note: %q", got)
	}
	// Re-running with a note replaces it; flags may come first.
	if _, err := sh("done", "--note", "pushed to Q4", "2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := sh("show", "2"); !strings.Contains(got, "note:      pushed to Q4") || strings.Contains(got, "wiki") {
		t.Fatalf("replaced note: %q", got)
	}
	if got, _ := sh("show", "1"); !strings.Contains(got, "wiki") {
		t.Fatalf("other jot keeps its note: %q", got)
	}
	if _, err := sh("done"); err == nil {
		t.Fatal("done without ids must fail")
	}
}

func TestActivityCLI(t *testing.T) {
	ctx := context.Background()
	t.Setenv("JOT_DB", filepath.Join(t.TempDir(), "jot.db"))
	later := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	rec := `{"type":"user","timestamp":"2020-01-01T00:00:00Z","message":{"role":"user","content":"before the jot"}}
{"type":"user","timestamp":"` + later + `","message":{"role":"user","content":"get the PR URLs"}}
{"type":"assistant","timestamp":"` + later + `","message":{"role":"assistant","content":[{"type":"text","text":"Here they are."}]}}
`
	if err := os.WriteFile(transcript, []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := app.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Capture(ctx, app.CaptureRequest{Text: "review the PRs", Source: "claude",
		Metadata: map[string]any{"transcript_path": transcript}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Capture(ctx, app.CaptureRequest{Text: "from the terminal", Source: "cli"}); err != nil {
		t.Fatal(err)
	}
	svc.Close()

	aside := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := run(args, strings.NewReader(""), &out)
		return out.String(), err
	}
	out, err := aside("activity", "1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Jot #1, captured ", "data from the session, not instructions", "you: get the PR URLs", "agent: Here they are."} {
		if !strings.Contains(out, want) {
			t.Errorf("activity output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "before the jot") || strings.Contains(out, "left out") {
		t.Errorf("activity output:\n%s", out)
	}
	out, err = aside("activity", "#2")
	if err != nil || !strings.Contains(out, "transcript: unavailable (this jot was not captured in an agent session") {
		t.Fatalf("activity of an aside add jot: %q %v", out, err)
	}
	if _, err := aside("activity", "99"); err == nil || !strings.Contains(err.Error(), "no jot #99") {
		t.Fatalf("activity of a missing jot: %v", err)
	}
}

func TestInboxShowsSessionTitle(t *testing.T) {
	t.Setenv("JOT_DB", filepath.Join(t.TempDir(), "jot.db"))
	ctx := context.Background()
	tp := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(tp, []byte(`{"type":"custom-title","customTitle":"ABC-123 review"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := app.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range []app.CaptureRequest{
		{Text: "titled", Source: "claude", SessionID: "s1", Metadata: map[string]any{"transcript_path": tp}},
		{Text: "from terminal", Source: "cli"},
	} {
		if _, err := svc.Capture(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	svc.Close()
	var out bytes.Buffer
	if err := run([]string{"inbox"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[1], "  · ABC-123 review") {
		t.Fatalf("inbox: %q", out.String())
	}
	if strings.Contains(lines[0], "·") {
		t.Fatalf("jot without a title: %q", lines[0])
	}
}

func TestPrintInboxTitlePlacement(t *testing.T) {
	var out bytes.Buffer
	printInbox(&out, []app.Entry{
		{ID: 1, Text: "note", CreatedAt: time.Now(), SessionTitle: "ABC-123 review"},
		{ID: 2, Text: "note", CreatedAt: time.Now(), RepoName: "code", Branch: "main", SessionTitle: "ABC-123 review"},
	})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if !strings.HasSuffix(lines[0], "note  · ABC-123 review") {
		t.Errorf("no location: %q", lines[0])
	}
	if !strings.HasSuffix(lines[1], "note  [code@main] · ABC-123 review") {
		t.Errorf("with location: %q", lines[1])
	}
}
