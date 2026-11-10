package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kacxx/jotaside/internal/store"
)

func TestWorkItem(t *testing.T) {
	cases := []struct {
		prompt, want string
	}{
		// Tracker keys (D4): uppercase letters and digits, a hyphen, digits,
		// as a whole word.
		{"ABC-123: test", "ABC-123"},
		{"look at SUP-4821 please", "SUP-4821"},
		{"P2-14 is next", "P2-14"},
		{"(ABC-1)", "ABC-1"},
		{"branch SUP-4821-fix", "SUP-4821"},
		{"https://example.atlassian.net/browse/BILL-42", "BILL-42"},
		{"hash it with SHA-256", "SHA-256"}, // a wrong label is cheap (D4)
		{"abc-123 lower case", ""},
		{"Abc-123 mixed case", ""},
		{"xABC-123 inside a word", ""},
		{"ABC-123x inside a word", ""},
		{"ABC-12_3", "ABC-12"}, // as find: _ ends a key (SUP-4821_cache)
		{"2026-11 is a date", ""},
		{"ABC- 12", ""},
		// owner/repo#N, as typed.
		{"kacxx/jotaside#32: build it", "kacxx/jotaside#32"},
		{"see kacxx/notebook#31.", "kacxx/notebook#31"},
		{"see my-org/my.repo_x#7", "my-org/my.repo_x#7"},
		{"kacxx/jotaside#32x", ""},
		{"jotaside#32 without owner", ""},
		{"ABC-1/repo#2: fix it", "ABC-1/repo#2"}, // an owner shaped like a key is still owner/repo#N
		// GitHub issue and PR URLs label as owner/repo#N (D7).
		{"/code-review high https://github.com/kacxx/jotaside/issues/32", "kacxx/jotaside#32"},
		{"review https://github.com/kacxx/jotaside/pull/31/files", "kacxx/jotaside#31"},
		{"http://www.github.com/kacxx/jotaside/pull/9", "kacxx/jotaside#9"},
		{"https://github.com/kacxx/jotaside/issues/32#issuecomment-1", "kacxx/jotaside#32"},
		{"https://github.com/kacxx/jotaside/tree/main", ""},
		{"https://example.com/kacxx/jotaside/issues/32", ""},
		// Several items: the first one in the prompt wins (D5).
		{"ABC-1 then kacxx/jotaside#2", "ABC-1"},
		{"kacxx/jotaside#2 then ABC-1", "kacxx/jotaside#2"},
		{"https://github.com/Org/ABC-1/pull/2 then XYZ-9", "Org/ABC-1#2"},
		// Leading <system-reminder> blocks are not the prompt.
		{"<system-reminder>ZZZ-9</system-reminder>\nwork on ABC-1", "ABC-1"},
		{"<system-reminder>ZZZ-9</system-reminder>\nno item here", ""},
		{"Refactor the session middleware", ""},
		{"", ""},
	}
	for _, c := range cases {
		got, ok := WorkItem(c.prompt)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("WorkItem(%q) = %q, %v; want %q", c.prompt, got, ok, c.want)
		}
	}
}

func labelTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	var b []byte
	for _, l := range lines {
		b = append(b, l+"\n"...)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFirstPrompt(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"no transcript path", "", true},
		{"transcript missing", filepath.Join(t.TempDir(), "gone.jsonl"), true},
		{"launch argument: only mode records", labelTranscript(t,
			`{"type":"mode","mode":"default"}`,
			`{"type":"permission-mode","permissionMode":"default"}`,
			`{"type":"session-owner"}`,
		), true},
		{"hook context only", labelTranscript(t,
			`{"type":"queue-operation"}`,
			`{"type":"attachment","attachment":{"type":"hook_additional_context","content":"\"type\":\"user\""}}`,
			``,
			`not json`,
		), true},
		{"earlier prompt", labelTranscript(t,
			`{"type":"attachment"}`,
			`{"type":"user","message":{"role":"user","content":"hello"}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		), false},
		{"user record without a trailing newline", func() string {
			p := filepath.Join(t.TempDir(), "s.jsonl")
			if err := os.WriteFile(p, []byte(`{"type":"user","message":{"content":"x"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}(), false},
		{"transcript is a directory", t.TempDir(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FirstPrompt(c.path); got != c.want {
				t.Fatalf("FirstPrompt = %v, want %v", got, c.want)
			}
		})
	}
}

func TestLabelSession(t *testing.T) {
	ctx := context.Background()
	svc := sessionService(t)
	req := CaptureRequest{Source: "claude", SessionID: "s1", Cwd: "/work/api", Text: "session: ABC-123"}

	e, saved, err := svc.LabelSession(ctx, req)
	if err != nil || !saved {
		t.Fatalf("LabelSession = %v, %v", saved, err)
	}
	if e.Status != store.StatusDone || e.Source != "claude" || e.SessionID != "s1" || e.Text != "session: ABC-123" {
		t.Fatalf("label entry: %+v", e)
	}
	got, err := svc.Show(ctx, e.ID)
	if err != nil || got.Status != store.StatusDone {
		t.Fatalf("stored status = %q (%v), want done", got.Status, err)
	}
	if !strings.Contains(string(got.Metadata), `"`+store.MetaDoneAt+`"`) {
		t.Fatalf("a label saved as done needs done_at, like any closed jot: %s", got.Metadata)
	}
	if inbox, _ := svc.Inbox(ctx, 0); len(inbox) != 0 {
		t.Fatalf("an automatic label must not add to the inbox: %+v", inbox)
	}

	// A request without a session id saves nothing.
	if _, saved, err := svc.LabelSession(ctx, CaptureRequest{Source: "claude", Text: "session: ABC-7"}); err != nil || saved {
		t.Fatalf("no session id: saved=%v err=%v; want nothing saved", saved, err)
	}

	// A session that already has a session: jot is left alone (D9).
	if _, saved, err := svc.LabelSession(ctx, CaptureRequest{Source: "claude", SessionID: "s1", Text: "session: XYZ-9"}); err != nil || saved {
		t.Fatalf("second label: saved=%v err=%v; want nothing saved", saved, err)
	}

	// A hand-typed label in another session, or by another agent with the same
	// id, doesn't count.
	jot(t, svc, "claude", "s2", "", "session: by hand")
	jot(t, svc, "codex", "s3", "", "session: codex label")
	jot(t, svc, "claude", "s4", "", "session:") // names nothing, so it isn't a label
	for _, id := range []string{"s3", "s4"} {
		if _, saved, err := svc.LabelSession(ctx, CaptureRequest{Source: "claude", SessionID: id, Text: "session: ABC-1"}); err != nil || !saved {
			t.Fatalf("%s: saved=%v err=%v; want saved", id, saved, err)
		}
	}
	if _, saved, _ := svc.LabelSession(ctx, CaptureRequest{Source: "claude", SessionID: "s2", Text: "session: ABC-1"}); saved {
		t.Fatal("s2 has a hand-typed label; it must not be overwritten")
	}

	// And find lists the labelled session with a resume command (D1's check).
	found, _, err := svc.Find(ctx, "ABC-123")
	if err != nil || len(found) != 1 || found[0].ID != "s1" {
		t.Fatalf("find ABC-123: %+v (%v)", found, err)
	}
	if found[0].LabelText() != "ABC-123" || found[0].ResumeCommand() == "" {
		t.Fatalf("label %q, resume %q", found[0].LabelText(), found[0].ResumeCommand())
	}
}

func TestCaptureStatus(t *testing.T) {
	ctx := context.Background()
	svc := sessionService(t)
	e, err := svc.Capture(ctx, CaptureRequest{Text: "x", Status: store.StatusDone})
	if err != nil || e.Status != store.StatusDone {
		t.Fatalf("status %q (%v), want done", e.Status, err)
	}
	e, err = svc.Capture(ctx, CaptureRequest{Text: "y"})
	if err != nil || e.Status != store.StatusInbox {
		t.Fatalf("status %q (%v), want inbox by default", e.Status, err)
	}
}
