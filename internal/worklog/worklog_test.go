package worklog

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kacxx/jotaside/internal/app"
)

// sample follows docs/worklog-format.md: an owned item with a due date, a delegated
// one, a done one, and one whose description has its own spaced em-dash.
const sample = `# Worklog — <owner>
last-reconciled: 2026-08-28

## Service outage follow-up
id: outage-followup · opened: 2026-08-24 · links: ABC-1234
Action items from last week's outage.

- [ ] Guard on runtime processing — @alex since 2026-08-31 · ABC-1290
- [ ] Incident retro written up — me · due 2026-09-02 · ABC-123
- [x] Assign the guard ticket — done 2026-08-31 · chat:0a1b2c3d

Notes kept under the items.

## Customer escalation: rule not firing
id: esc-6784 · opened: 2026-08-27 · links: ESC-6784 · nudge: 2d
- [ ] Chase the customer, see https://github.com/acme/tools/issues/7 — me · due 2026-09-01 · ABC-123
- [ ] Phase A — parser rewrite
`

const (
	sessionA = "0a1b2c3d-1111-4222-8333-444455556666"
	sessionB = "9f8e7d6c-aaaa-4bbb-8ccc-ddddeeeeffff"
	today    = "2026-09-03"
)

func TestItems(t *testing.T) {
	items := Items(sample)
	if len(items) != 5 {
		t.Fatalf("got %d items, want 5", len(items))
	}
	if it := items[0]; it.Workstream != "Service outage follow-up" || it.WorkstreamID != "outage-followup" || !it.Open {
		t.Errorf("first item: %+v", it)
	}
	if it := items[2]; it.Open || !reflect.DeepEqual(it.Stamps(), []string{"0a1b2c3d"}) {
		t.Errorf("third item: %+v, stamps %v", it, it.Stamps())
	}
	if it := items[3]; it.WorkstreamID != "esc-6784" || !reflect.DeepEqual(it.Keys(), []string{"acme/tools#7", "ABC-123"}) {
		t.Errorf("fourth item: %+v, keys %v", it, it.Keys())
	}
	for _, it := range items {
		if p := it.Problem(); p != "" {
			t.Errorf("%q: %s", it.Text, p)
		}
	}
}

func TestProblem(t *testing.T) {
	for text, open := range map[string]bool{
		"Bad due date — me · due 2026-13-45": true,
		"Closed but no closure date":         false,
		"Closed, a key only — ABC-1":         false,
	} {
		if (Item{Open: open, Text: text}).Problem() == "" {
			t.Errorf("%q (open %v) parsed", text, open)
		}
	}
}

func TestStampsAreFacts(t *testing.T) {
	it := Item{Text: "see chat:0a1b2c3d — me · webchat:0a1b2c3d · chat:short · ABC-1 chat:0A1B2C3D-1111 · chat:0a1b2c3d-1111 · chat:9f8e7d6cxyz"}
	if got, want := it.Stamps(), []string{"0a1b2c3d-1111"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFindMatchesAKeyAsAWholeWord(t *testing.T) {
	if got := len(Find(sample, "abc-12")); got != 0 {
		t.Errorf("abc-12 matched %d items, want 0", got)
	}
	if got := len(Find(sample, "ABC-123")); got != 2 {
		t.Errorf("ABC-123 matched %d items, want 2", got)
	}
	if got := len(Find(sample, "retro")); got != 1 {
		t.Errorf("retro matched %d items, want 1", got)
	}
}

func TestAdd(t *testing.T) {
	out, err := Add(sample, "outage-followup", "Tell support — due 2026-09-05 · ABC-125", sessionA, today)
	if err != nil {
		t.Fatal(err)
	}
	want := "- [x] Assign the guard ticket — done 2026-08-31 · chat:0a1b2c3d\n" +
		"- [ ] Tell support — me · due 2026-09-05 · ABC-125 · chat:0a1b2c3d\n\nNotes kept under the items."
	if !strings.Contains(out, want) {
		t.Errorf("item not added after the workstream's last item, with an owner and a stamp fact:\n%s", out)
	}
	if strings.Count(out, "\n") != strings.Count(sample, "\n")+1 {
		t.Errorf("add changed more than one line")
	}

	for _, tc := range []struct{ ws, text, session, want string }{
		// By part of a title; a person with no date is waited on since today.
		{"rule not firing", "Ask for logs — @sam", "", "- [ ] Ask for logs — @sam since 2026-09-03\n"},
		{"esc-6784", "Ask for logs — ABC-7 · @sam since 2026-09-01", sessionB, "- [ ] Ask for logs — @sam since 2026-09-01 · ABC-7 · chat:9f8e7d6c\n"},
		// A spaced em-dash with no fact after it is part of the description.
		{"ESC-6784", "Phase B — reader rewrite", "", "- [ ] Phase B — reader rewrite — me\n"},
		{"esc-6784", "Already stamped — chat:0a1b2c3d-1111", sessionA, "- [ ] Already stamped — me · chat:0a1b2c3d-1111\n"},
	} {
		out, err := Add(sample, tc.ws, tc.text, tc.session, today)
		if err != nil {
			t.Errorf("Add(%q, %q): %v", tc.ws, tc.text, err)
		} else if !strings.HasSuffix(out, "- [ ] Phase A — parser rewrite\n"+tc.want) {
			t.Errorf("Add(%q, %q): want the log to end with %q:\n%s", tc.ws, tc.text, tc.want, out)
		}
	}

	for _, tc := range []struct{ ws, text, wantErr string }{
		{"nowhere", "x", "no workstream matches"},
		{"e", "x", "more than one workstream"},
		{"esc-6784", " ", "no text"},
		{"esc-6784", "— me", "no text"},
		{"esc-6784", "two\nlines", "one line"},
		{"esc-6784", "x — due 2026-13-45", "isn't a date"},
		{"esc-6784", "x — @sam since yesterday", "isn't a date"},
		{"esc-6784", "x — done 2026-09-01", "open item"},
		{"esc-6784", "x — me · @sam", "one owner"},
	} {
		if _, err := Add(sample, tc.ws, tc.text, sessionA, today); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("Add(%q, %q): got %v, want an error with %q", tc.ws, tc.text, err, tc.wantErr)
		}
	}
}

func TestAddKeepsLineEndings(t *testing.T) {
	crlf := strings.ReplaceAll(sample, "\n", "\r\n")
	out, err := Add(crlf, "esc-6784", "x", "", today)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Errorf("a bare line feed was written into a CRLF log")
	}
	if !strings.HasSuffix(out, "- [ ] x — me\r\n") {
		t.Errorf("item not added to a CRLF log:\n%q", out)
	}
}

func TestAddToAWorkstreamWithNoItems(t *testing.T) {
	out, err := Add("# Worklog\n\n## New thing\nid: new-thing · opened: 2026-09-03\n\n## Other\n", "new-thing", "First", "", today)
	if err != nil {
		t.Fatal(err)
	}
	if want := "id: new-thing · opened: 2026-09-03\n- [ ] First — me\n\n## Other\n"; !strings.HasSuffix(out, want) {
		t.Errorf("got:\n%s", out)
	}
}

func TestClose(t *testing.T) {
	for _, tc := range []struct{ q, session, want string }{
		// me and the due date go; the key stays; the stamp is one more fact.
		{"retro", sessionB, "- [x] Incident retro written up — done 2026-09-03 · ABC-123 · chat:9f8e7d6c\n"},
		// A delegated item keeps who it waited on.
		{"Guard on runtime", sessionB, "- [x] Guard on runtime processing — @alex since 2026-08-31 · done 2026-09-03 · ABC-1290 · chat:9f8e7d6c\n"},
		{"Phase A", "", "- [x] Phase A — parser rewrite — done 2026-09-03\n"},
	} {
		out, it, err := Close(sample, tc.q, tc.session, today)
		if err != nil {
			t.Errorf("Close(%q): %v", tc.q, err)
			continue
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("Close(%q): want %q in:\n%s", tc.q, tc.want, out)
		}
		if it.Open || it.Problem() != "" {
			t.Errorf("Close(%q) returned %+v (%s)", tc.q, it, it.Problem())
		}
		if strings.Count(out, "\n") != strings.Count(sample, "\n") {
			t.Errorf("Close(%q) changed the number of lines", tc.q)
		}
	}
	if _, _, err := Close(sample, "ABC-123", sessionB, today); err == nil || !strings.Contains(err.Error(), "2 open items") {
		t.Errorf("closing an ambiguous key: got %v", err)
	}
	if _, _, err := Close(sample, "Assign the guard", sessionB, today); err == nil || !strings.Contains(err.Error(), "no open item") {
		t.Errorf("closing a done item: got %v", err)
	}
	// The session that added an item doesn't stamp it twice on close.
	added, _ := Add(sample, "esc-6784", "Chase the invoice", sessionA, today)
	out, _, err := Close(added, "Chase the invoice", sessionA, today)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "- [x] Chase the invoice — done 2026-09-03 · chat:0a1b2c3d\n") {
		t.Errorf("stamped twice, or not closed:\n%s", out)
	}
	// A line that doesn't parse is reported, not rewritten.
	bad := sample + "- [ ] Bad due date — me · due 2026-13-45\n"
	if _, _, err := Close(bad, "Bad due date", sessionA, today); err == nil || !strings.Contains(err.Error(), "doesn't parse") {
		t.Errorf("closing a line that doesn't parse: got %v", err)
	}
}

// repo makes a git repo holding the sample log, and a Claude Code folder with
// a transcript for sessionA that started in startDir.
func repo(t *testing.T) (file, claude, startDir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	file = filepath.Join(dir, "ledger.md")
	if err := os.WriteFile(file, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
		{"add", "ledger.md"},
		{"commit", "-q", "-m", "start"},
	} {
		if _, err := git(dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	claude = t.TempDir()
	startDir = filepath.Join(t.TempDir(), "proj")
	proj := filepath.Join(claude, "projects", "some-project")
	if err := os.MkdirAll(proj, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := `{"type":"user","cwd":` + quoteJSON(startDir) + `}` + "\n"
	if err := os.WriteFile(filepath.Join(proj, sessionA+".jsonl"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	return file, claude, startDir
}

func quoteJSON(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }

// caller is the surroundings of one worklog command.
type caller struct {
	file, claude    string
	session, cursor string // the Claude Code and Cursor session IDs in the environment
	find            func(context.Context, string) ([]app.Session, []app.Entry, error)
	findEach        func(context.Context, []string) ([]app.Found, error)
	sessions        func(context.Context) ([]app.Session, error)
}

func (c caller) run(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	vars := map[string]string{
		"WORKLOG_LEDGER": c.file, "CLAUDE_CONFIG_DIR": c.claude,
		"CLAUDE_CODE_SESSION_ID": c.session, "CURSOR_CONVERSATION_ID": c.cursor,
	}
	env := Env{
		Getenv:   func(k string) string { return vars[k] },
		Home:     t.TempDir(),
		Now:      func() time.Time { return time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC) },
		Find:     c.find,
		FindEach: c.findEach,
		Sessions: c.sessions,
	}
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, env, &out, &errb)
	return out.String(), errb.String(), code
}

func TestAddCloseShow(t *testing.T) {
	file, claude, startDir := repo(t)
	dir := filepath.Dir(file)
	a := caller{file: file, claude: claude, session: sessionA}
	b := caller{file: file, claude: claude, session: sessionB}

	if out, errs, code := a.run(t, "add", "esc-6784", "Send the summary", "—", "ABC-200"); code != 0 || errs != "" {
		t.Fatalf("add: code %d, out %q, err %q", code, out, errs)
	}
	got, _ := os.ReadFile(file)
	if !strings.HasSuffix(string(got), "- [ ] Send the summary — me · ABC-200 · chat:0a1b2c3d\n") {
		t.Fatalf("log after add:\n%s", got)
	}
	if log, _ := git(dir, "log", "--format=%s"); !strings.HasPrefix(log, "worklog add: Send the summary — ABC-200\n") {
		t.Errorf("commit subjects: %q", log)
	}
	if st, _ := git(dir, "status", "--porcelain"); st != "" {
		t.Errorf("uncommitted after add: %q", st)
	}

	out, _, code := b.run(t, "show", "ABC-200")
	if code != 0 {
		t.Fatalf("show: code %d", code)
	}
	resume := app.Session{Source: "claude", ID: sessionA, Cwd: startDir}.ResumeCommand()
	if !strings.Contains(out, "chat:0a1b2c3d: "+resume) || !strings.Contains(out, "Customer escalation: rule not firing (esc-6784)") {
		t.Errorf("show output:\n%s\nwant the resume command %q", out, resume)
	}

	// A different session closes it; both are now on the item, and the one
	// with no transcript says so.
	if _, errs, code := b.run(t, "close", "ABC-200"); code != 0 {
		t.Fatalf("close: code %d, err %q", code, errs)
	}
	out, _, _ = caller{file: file, claude: claude}.run(t, "show", "Send the summary")
	if !strings.Contains(out, "[x] Send the summary — done 2026-09-03 · ABC-200 · chat:0a1b2c3d · chat:9f8e7d6c\n") ||
		!strings.Contains(out, "chat:9f8e7d6c: session not found") {
		t.Errorf("show after close:\n%s", out)
	}
}

func TestCursorStamp(t *testing.T) {
	file, claude, _ := repo(t)
	label := app.Entry{Text: "session: ABC-300"}
	c := caller{file: file, claude: claude, cursor: sessionB, sessions: func(context.Context) ([]app.Session, error) {
		return []app.Session{
			{Source: "cursor", ID: sessionB, Jots: []app.Entry{label}, Label: label},
			{Source: "cursor", ID: "ffffffff-0000-4000-8000-000000000000", Jots: []app.Entry{label}, Label: label},
		}, nil
	}}
	if _, errs, code := c.run(t, "add", "esc-6784", "Written from Cursor"); code != 0 || errs != "" {
		t.Fatalf("add: code %d, err %q", code, errs)
	}
	got, _ := os.ReadFile(file)
	if !strings.HasSuffix(string(got), "- [ ] Written from Cursor — me · chat:9f8e7d6c\n") {
		t.Fatalf("log after add:\n%s", got)
	}
	out, _, _ := c.run(t, "show", "Written from Cursor")
	if !strings.Contains(out, "chat:9f8e7d6c (ABC-300): session "+sessionB+", no resume command") || strings.Contains(out, "ffffffff") {
		t.Errorf("show output:\n%s", out)
	}
}

func TestShowFallsBackToJotaside(t *testing.T) {
	file, claude, _ := repo(t)
	find := func(_ context.Context, q string) ([]app.Session, []app.Entry, error) {
		if q != "ABC-123" {
			return nil, nil, nil
		}
		label := app.Entry{Text: "session: ABC-123"}
		return []app.Session{{Source: "claude", ID: sessionB, Cwd: "/work/x", Jots: []app.Entry{label}, Label: label}}, nil, nil
	}
	out, _, code := caller{file: file, claude: claude, find: find}.run(t, "brief")
	if code != 0 {
		t.Fatalf("brief: code %d", code)
	}
	if strings.Contains(out, "Assign the guard") {
		t.Errorf("brief lists a done item:\n%s", out)
	}
	if n := strings.Count(out, "ABC-123 (ABC-123): "); n != 2 {
		t.Errorf("want the fallback session on both open items naming ABC-123, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "claude --resume "+sessionB) {
		t.Errorf("no resume command for the fallback session:\n%s", out)
	}
}

// brief asks for every key in one FindEach, which reads the transcripts once,
// instead of a Find per key (#35).
func TestBriefFindsKeysAtOnce(t *testing.T) {
	file, claude, _ := repo(t)
	find := func(_ context.Context, q string) ([]app.Session, []app.Entry, error) {
		t.Errorf("Find(%q) called; want FindEach", q)
		return nil, nil, nil
	}
	var calls [][]string
	findEach := func(_ context.Context, qs []string) ([]app.Found, error) {
		calls = append(calls, qs)
		out := make([]app.Found, len(qs))
		for i, q := range qs {
			if q == "ABC-123" {
				out[i].Sessions = []app.Session{{Source: "claude", ID: sessionB, Cwd: "/work/x"}}
			}
		}
		return out, nil
	}
	out, _, code := caller{file: file, claude: claude, find: find, findEach: findEach}.run(t, "brief")
	if code != 0 {
		t.Fatalf("brief: code %d", code)
	}
	if len(calls) != 1 {
		t.Fatalf("FindEach called %d times, want once", len(calls))
	}
	seen := map[string]bool{}
	for _, k := range calls[0] {
		if seen[k] {
			t.Errorf("key %s asked for twice: %v", k, calls[0])
		}
		seen[k] = true
	}
	if !seen["ABC-123"] {
		t.Errorf("keys %v lack ABC-123", calls[0])
	}
	if n := strings.Count(out, "ABC-123: cd /work/x && claude --resume "+sessionB); n != 2 {
		t.Errorf("want the session on both open items naming ABC-123, got %d:\n%s", n, out)
	}
}

func TestWriteRefusals(t *testing.T) {
	t.Run("a remote", func(t *testing.T) {
		file, claude, _ := repo(t)
		if _, err := git(filepath.Dir(file), "remote", "add", "origin", "https://example.com/x.git"); err != nil {
			t.Fatal(err)
		}
		_, errs, code := caller{file: file, claude: claude, session: sessionA}.run(t, "add", "esc-6784", "x")
		if code == 0 || !strings.Contains(errs, "has a remote (origin)") {
			t.Errorf("code %d, err %q", code, errs)
		}
		if got, _ := os.ReadFile(file); string(got) != sample {
			t.Errorf("the log changed")
		}
	})
	t.Run("uncommitted changes", func(t *testing.T) {
		file, claude, _ := repo(t)
		edited := sample + "- [ ] someone else's line\n"
		if err := os.WriteFile(file, []byte(edited), 0o600); err != nil {
			t.Fatal(err)
		}
		_, errs, code := caller{file: file, claude: claude, session: sessionA}.run(t, "close", "retro")
		if code == 0 || !strings.Contains(errs, "uncommitted changes") {
			t.Errorf("code %d, err %q", code, errs)
		}
		if got, _ := os.ReadFile(file); string(got) != edited {
			t.Errorf("the log changed")
		}
	})
	t.Run("not a git repo", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "ledger.md")
		if err := os.WriteFile(file, []byte(sample), 0o600); err != nil {
			t.Fatal(err)
		}
		// A parent folder may be a repo on a developer's machine; skip there.
		if _, err := git(filepath.Dir(file), "rev-parse", "--git-dir"); err == nil {
			t.Skip("the temp folder is inside a git repo")
		}
		_, errs, code := caller{file: file, claude: t.TempDir(), session: sessionA}.run(t, "add", "esc-6784", "x")
		if code == 0 || !strings.Contains(errs, "isn't in a git repo") {
			t.Errorf("code %d, err %q", code, errs)
		}
	})
	t.Run("a failed commit puts the log back", func(t *testing.T) {
		file, claude, _ := repo(t)
		hook := filepath.Join(filepath.Dir(file), ".git", "hooks", "pre-commit")
		if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		_, errs, code := caller{file: file, claude: claude, session: sessionA}.run(t, "add", "esc-6784", "x")
		if code == 0 || !strings.Contains(errs, "put back as it was") {
			t.Errorf("code %d, err %q", code, errs)
		}
		if got, _ := os.ReadFile(file); string(got) != sample {
			t.Errorf("the log wasn't put back:\n%s", got)
		}
	})
	t.Run("no session ID", func(t *testing.T) {
		file, claude, _ := repo(t)
		_, errs, code := caller{file: file, claude: claude}.run(t, "add", "esc-6784", "x")
		if code != 0 || !strings.Contains(errs, "no chat: stamp") {
			t.Errorf("code %d, err %q", code, errs)
		}
	})
}
