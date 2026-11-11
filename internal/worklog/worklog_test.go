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

	"github.com/kacxx/jotaside/internal/app"
)

const sample = `# Worklog

## Checkout rewrite
links: ABC-100

- [ ] Draft the rollout note · due 2026-11-14 · ABC-123
- [x] Agree the cutover window · ABC-124 chat:0a1b2c3d

Notes kept under the items.

## Billing export
- [ ] Ask about the nightly job, see https://github.com/acme/tools/issues/7 and ABC-123
`

const (
	sessionA = "0a1b2c3d-1111-4222-8333-444455556666"
	sessionB = "9f8e7d6c-aaaa-4bbb-8ccc-ddddeeeeffff"
)

func TestItems(t *testing.T) {
	items := Items(sample)
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3", len(items))
	}
	if it := items[0]; it.Workstream != "Checkout rewrite" || !it.Open || !strings.HasPrefix(it.Text, "Draft the rollout note") {
		t.Errorf("first item: %+v", it)
	}
	if it := items[1]; it.Open || !reflect.DeepEqual(it.Stamps(), []string{"0a1b2c3d"}) {
		t.Errorf("second item: %+v, stamps %v", it, it.Stamps())
	}
	if got, want := items[2].Keys(), []string{"acme/tools#7", "ABC-123"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys: got %v, want %v", got, want)
	}
}

func TestStampsIgnoreLookalikes(t *testing.T) {
	it := Item{Text: "see webchat:0a1b2c3d and chat:short and chat:0A1B2C3D-1111 chat:0a1b2c3d-1111"}
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
	if got := len(Find(sample, "cutover")); got != 1 {
		t.Errorf("cutover matched %d items, want 1", got)
	}
}

func TestAdd(t *testing.T) {
	out, err := Add(sample, "checkout", "Tell support · ABC-125", sessionA)
	if err != nil {
		t.Fatal(err)
	}
	want := "- [x] Agree the cutover window · ABC-124 chat:0a1b2c3d\n- [ ] Tell support · ABC-125 chat:0a1b2c3d\n\nNotes kept under the items."
	if !strings.Contains(out, want) {
		t.Errorf("item not added after the workstream's last item:\n%s", out)
	}
	if strings.Count(out, "\n") != strings.Count(sample, "\n")+1 {
		t.Errorf("add changed more than one line")
	}

	out, err = Add(sample, "Billing export", "No session here", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "- [ ] No session here\n") {
		t.Errorf("unstamped item not at the end of the last workstream:\n%s", out)
	}

	for _, tc := range []struct{ ws, text, wantErr string }{
		{"nowhere", "x", "no workstream matches"},
		{"e", "x", "more than one workstream"},
		{"checkout", " ", "no text"},
		{"checkout", "two\nlines", "one line"},
	} {
		if _, err := Add(sample, tc.ws, tc.text, sessionA); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("Add(%q, %q): got %v, want an error with %q", tc.ws, tc.text, err, tc.wantErr)
		}
	}
}

func TestAddKeepsLineEndingsAndRefusesAnUnknownGrammar(t *testing.T) {
	crlf := strings.ReplaceAll(sample, "\n", "\r\n")
	out, err := Add(crlf, "billing", "x", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Errorf("a bare line feed was written into a CRLF log")
	}
	if _, err := Add("## Stream\n* TODO something\n", "stream", "x", ""); err == nil {
		t.Errorf("add wrote to a log with no recognised item")
	}
}

func TestClose(t *testing.T) {
	out, it, err := Close(sample, "rollout note", sessionB)
	if err != nil {
		t.Fatal(err)
	}
	if want := "- [x] Draft the rollout note · due 2026-11-14 · ABC-123 chat:9f8e7d6c\n"; !strings.Contains(out, want) {
		t.Errorf("not closed and stamped:\n%s", out)
	}
	if it.Open {
		t.Errorf("returned item still open")
	}
	if _, _, err := Close(sample, "ABC-123", sessionB); err == nil || !strings.Contains(err.Error(), "2 open items") {
		t.Errorf("closing an ambiguous key: got %v", err)
	}
	if _, _, err := Close(sample, "ABC-124", sessionB); err == nil || !strings.Contains(err.Error(), "no open item") {
		t.Errorf("closing a done item: got %v", err)
	}
	// The session that added an item doesn't stamp it twice on close.
	added, _ := Add(sample, "billing", "Chase the invoice", sessionA)
	out, _, err = Close(added, "Chase the invoice", sessionA)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "- [x] Chase the invoice chat:0a1b2c3d\n") {
		t.Errorf("stamped twice, or not closed:\n%s", out)
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

func runIn(t *testing.T, file, claude, session string, find func(context.Context, string) ([]app.Session, []app.Entry, error), args ...string) (string, string, int) {
	t.Helper()
	vars := map[string]string{"WORKLOG_FILE": file, "CLAUDE_CONFIG_DIR": claude, "CLAUDE_CODE_SESSION_ID": session}
	var out, errb bytes.Buffer
	code := Run(context.Background(), args, Env{Getenv: func(k string) string { return vars[k] }, Home: t.TempDir(), Find: find}, &out, &errb)
	return out.String(), errb.String(), code
}

func TestAddCloseShow(t *testing.T) {
	file, claude, startDir := repo(t)
	dir := filepath.Dir(file)

	if out, errs, code := runIn(t, file, claude, sessionA, nil, "add", "billing", "Send the summary", "ABC-200"); code != 0 || errs != "" {
		t.Fatalf("add: code %d, out %q, err %q", code, out, errs)
	}
	got, _ := os.ReadFile(file)
	if !strings.HasSuffix(string(got), "- [ ] Send the summary ABC-200 chat:0a1b2c3d\n") {
		t.Fatalf("log after add:\n%s", got)
	}
	if log, _ := git(dir, "log", "--format=%s"); !strings.HasPrefix(log, "worklog add: Send the summary ABC-200\n") {
		t.Errorf("commit subjects: %q", log)
	}
	if st, _ := git(dir, "status", "--porcelain"); st != "" {
		t.Errorf("uncommitted after add: %q", st)
	}

	out, _, code := runIn(t, file, claude, sessionB, nil, "show", "ABC-200")
	if code != 0 {
		t.Fatalf("show: code %d", code)
	}
	resume := app.Session{Source: "claude", ID: sessionA, Cwd: startDir}.ResumeCommand()
	if !strings.Contains(out, "chat:0a1b2c3d: "+resume) || !strings.Contains(out, "Billing export") {
		t.Errorf("show output:\n%s\nwant the resume command %q", out, resume)
	}

	// A different session closes it; both are now on the item, and the one
	// with no transcript says so.
	if _, errs, code := runIn(t, file, claude, sessionB, nil, "close", "ABC-200"); code != 0 {
		t.Fatalf("close: code %d, err %q", code, errs)
	}
	out, _, _ = runIn(t, file, claude, "", nil, "show", "Send the summary")
	if !strings.Contains(out, "[x] Send the summary") || !strings.Contains(out, "chat:9f8e7d6c: transcript not found") {
		t.Errorf("show after close:\n%s", out)
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
	out, _, code := runIn(t, file, claude, "", find, "brief")
	if code != 0 {
		t.Fatalf("brief: code %d", code)
	}
	if strings.Contains(out, "cutover") {
		t.Errorf("brief lists a done item:\n%s", out)
	}
	if n := strings.Count(out, "ABC-123 (ABC-123): "); n != 2 {
		t.Errorf("want the fallback session on both open items naming ABC-123, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "claude --resume "+sessionB) {
		t.Errorf("no resume command for the fallback session:\n%s", out)
	}
}

func TestWriteRefusals(t *testing.T) {
	t.Run("a remote", func(t *testing.T) {
		file, claude, _ := repo(t)
		if _, err := git(filepath.Dir(file), "remote", "add", "origin", "https://example.com/x.git"); err != nil {
			t.Fatal(err)
		}
		_, errs, code := runIn(t, file, claude, sessionA, nil, "add", "billing", "x")
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
		_, errs, code := runIn(t, file, claude, sessionA, nil, "close", "rollout")
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
		_, errs, code := runIn(t, file, t.TempDir(), sessionA, nil, "add", "billing", "x")
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
		_, errs, code := runIn(t, file, claude, sessionA, nil, "add", "billing", "x")
		if code == 0 || !strings.Contains(errs, "put back as it was") {
			t.Errorf("code %d, err %q", code, errs)
		}
		if got, _ := os.ReadFile(file); string(got) != sample {
			t.Errorf("the log wasn't put back:\n%s", got)
		}
	})
	t.Run("no session ID", func(t *testing.T) {
		file, claude, _ := repo(t)
		_, errs, code := runIn(t, file, claude, "", nil, "add", "billing", "x")
		if code != 0 || !strings.Contains(errs, "no chat: stamp") {
			t.Errorf("code %d, err %q", code, errs)
		}
	})
}
