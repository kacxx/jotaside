package hooks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kacxx/jotaside/internal/app"
	"github.com/kacxx/jotaside/internal/store"
)

const labelSession = "3f1c2a9e-7b4d-4e0a-9c55-0d2f8e6b1a47" // the claude fixtures' session_id

// allEntries returns every entry, any status, oldest first.
func allEntries(t *testing.T, path string) []app.Entry {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		return nil // never opened
	}
	svc, err := app.Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	ss, err := svc.Sessions(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	var es []app.Entry
	for _, s := range ss {
		es = append(es, s.Jots...)
	}
	return es
}

// firstPrompt is a claude prompt payload whose transcript doesn't exist yet,
// as on a session's first typed prompt.
func firstPrompt(t *testing.T, prompt string) []byte {
	t.Helper()
	return fixture(t, "claude", "normal", map[string]any{
		"prompt":          prompt,
		"cwd":             t.TempDir(),
		"transcript_path": filepath.Join(t.TempDir(), "missing.jsonl"),
	})
}

func TestClaudeFirstPromptLabelsSession(t *testing.T) {
	cases := []struct{ name, prompt, want string }{
		{"tracker key", "ABC-123: test", "session: ABC-123"},
		{"issue URL", "/code-review high https://github.com/kacxx/jotaside/issues/32", "session: kacxx/jotaside#32"},
		{"PR URL", "review https://github.com/kacxx/jotaside/pull/31", "session: kacxx/jotaside#31"},
		{"owner/repo#N", "kacxx/jotaside#32: read and do the task", "session: kacxx/jotaside#32"},
		{"first of several", "SUP-1 blocks kacxx/jotaside#2 and ABC-3", "session: SUP-1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(envEntrypoint, "cli")
			open, path := tempDB(t)
			if out, b := runClaude(t, firstPrompt(t, c.prompt), open); out != "" || b != nil {
				t.Fatalf("the prompt must go through silently; got %q", out)
			}
			es := allEntries(t, path)
			if len(es) != 1 {
				t.Fatalf("got %d entries, want 1", len(es))
			}
			e := es[0]
			if e.Text != c.want || e.Status != store.StatusDone || e.Source != "claude" || e.SessionID != labelSession {
				t.Fatalf("label entry: %+v", e)
			}
			if len(entries(t, path)) != 0 {
				t.Fatal("an automatic label must not be in the inbox")
			}
		})
	}
}

func TestClaudeFirstPromptLaunchArgTranscript(t *testing.T) {
	// A prompt passed at launch finds a transcript holding only mode records.
	t.Setenv(envEntrypoint, "cli")
	tr := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(tr, []byte(`{"type":"mode"}`+"\n"+`{"type":"permission-mode"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	open, path := tempDB(t)
	runClaude(t, fixture(t, "claude", "normal", map[string]any{"prompt": "ABC-123: test", "transcript_path": tr, "cwd": t.TempDir()}), open)
	if es := allEntries(t, path); len(es) != 1 || es[0].Text != "session: ABC-123" {
		t.Fatalf("entries: %+v", es)
	}
}

func TestClaudeLaterPromptSavesNothing(t *testing.T) {
	t.Setenv(envEntrypoint, "cli")
	tr := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(tr, []byte(`{"type":"user","message":{"content":"hello"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := runClaude(t, fixture(t, "claude", "normal", map[string]any{"prompt": "now ABC-123", "transcript_path": tr}), neverOpen(t))
	if out != "" {
		t.Fatalf("got %q", out)
	}
}

func TestClaudeNoLabel(t *testing.T) {
	// Headless and Agent SDK runs (D6, revised).
	for _, ep := range []string{"sdk-cli", "sdk-ts", "sdk-py"} {
		t.Run("entrypoint "+ep, func(t *testing.T) {
			t.Setenv(envEntrypoint, ep)
			if out, _ := runClaude(t, firstPrompt(t, "ABC-123: test"), neverOpen(t)); out != "" {
				t.Fatalf("got %q", out)
			}
		})
	}
	t.Run("no work item", func(t *testing.T) {
		t.Setenv(envEntrypoint, "cli")
		if out, _ := runClaude(t, firstPrompt(t, "Refactor the session middleware"), neverOpen(t)); out != "" {
			t.Fatalf("got %q", out)
		}
	})
	t.Run("hand-typed label first", func(t *testing.T) {
		// Jots are blocked, so they never reach the transcript: a session
		// whose first jot was ">> session: …" still has a first prompt.
		t.Setenv(envEntrypoint, "cli")
		open, path := tempDB(t)
		_, b := runClaude(t, fixture(t, "claude", "capture", map[string]any{
			"prompt": ">> session: by hand", "cwd": t.TempDir()}), open)
		assertBlockedOK(t, b, 1)
		runClaude(t, firstPrompt(t, "ABC-123: test"), open)
		es := allEntries(t, path)
		if len(es) != 1 || es[0].Text != "session: by hand" {
			t.Fatalf("entries: %+v", es)
		}
	})
	t.Run("a jot naming a work item is a jot", func(t *testing.T) {
		t.Setenv(envEntrypoint, "cli")
		open, path := tempDB(t)
		_, b := runClaude(t, firstPrompt(t, ">> check ABC-123 later"), open)
		assertBlockedOK(t, b, 1)
		if es := entries(t, path); len(es) != 1 || es[0].Text != "check ABC-123 later" {
			t.Fatalf("entries: %+v", es)
		}
	})
	t.Run("database fails", func(t *testing.T) {
		t.Setenv(envEntrypoint, "cli")
		fail := func() (*app.Service, error) { return nil, os.ErrPermission }
		if out, b := runClaude(t, firstPrompt(t, "ABC-123: test"), fail); out != "" || b != nil {
			t.Fatalf("a failed label must not block or print; got %q", out)
		}
	})
	t.Run("opener panics", func(t *testing.T) {
		t.Setenv(envEntrypoint, "cli")
		boom := func() (*app.Service, error) { panic("boom") }
		if out, b := runClaude(t, firstPrompt(t, "ABC-123: test"), boom); out != "" || b != nil {
			t.Fatalf("a panic while labelling must not block or print; got %q", out)
		}
	})
}

func TestCodexAndCursorNeverLabel(t *testing.T) {
	t.Setenv(envEntrypoint, "cli")
	if out, _ := runCodex(t, fixture(t, "codex", "normal", map[string]any{
		"prompt": "ABC-123: test", "transcript_path": filepath.Join(t.TempDir(), "missing.jsonl")}), neverOpen(t)); out != "" {
		t.Fatalf("codex: got %q", out)
	}
	if _, o := runCursor(t, fixture(t, "cursor", "normal", map[string]any{"prompt": "ABC-123: test"}), neverOpen(t)); !o.Continue {
		t.Fatal("cursor: should continue")
	}
	// Cursor's prompt event sent to the Claude Code hook is handled as Cursor.
	if _, o := runClaudeAsCursor(t, fixture(t, "cursor", "normal", map[string]any{"prompt": "ABC-123: test"}), neverOpen(t)); !o.Continue {
		t.Fatal("cursor via claude hook: should continue")
	}
}
