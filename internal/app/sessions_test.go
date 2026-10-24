package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kacxx/jotaside/internal/gitctx"
)

func sessionService(t *testing.T) *Service {
	t.Helper()
	svc, err := Open(filepath.Join(t.TempDir(), "jot.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	svc.git = func(context.Context, string) gitctx.Info { return gitctx.Info{} }
	return svc
}

func jot(t *testing.T, svc *Service, source, session, cwd, text string) {
	t.Helper()
	if _, err := svc.Capture(context.Background(), CaptureRequest{Text: text, Source: source, SessionID: session, Cwd: cwd}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsGroupAndLabel(t *testing.T) {
	ctx := context.Background()
	svc := sessionService(t)
	jot(t, svc, "claude", "s1", "/work/api", "first thought")                         // #1
	jot(t, svc, "codex", "s2", "/work/web", "codex idea")                             // #2
	jot(t, svc, "claude", "s1", "/elsewhere", "Session: SUP-4821 token TTL")          // #3
	jot(t, svc, "cli", "", "/work/api", "from the terminal")                          // #4
	jot(t, svc, "codex", "s1", "/work/api", "same id, different agent")               // #5
	jot(t, svc, "claude", "s1", "", "session: SUP-4821 token TTL, take two\ndetails") // #6

	ss, err := svc.Sessions(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 3 {
		t.Fatalf("got %d sessions, want 3 (jots without a session are left out)", len(ss))
	}
	// Most recently active first.
	if ss[0].Source != "claude" || ss[1].Source != "codex" || ss[1].ID != "s1" || ss[2].ID != "s2" {
		t.Fatalf("order: %+v", ss)
	}
	s := ss[0]
	if len(s.Jots) != 3 || s.Jots[0].ID != 1 || s.Latest().ID != 6 {
		t.Fatalf("claude s1 jots: %+v", s.Jots)
	}
	if s.Label.ID != 6 || s.LabelText() != "SUP-4821 token TTL, take two" {
		t.Fatalf("label: #%d %q", s.Label.ID, s.LabelText())
	}
	if s.Cwd != "/work/api" {
		t.Fatalf("cwd should come from the first jot, got %q", s.Cwd)
	}
	if ss[2].LabelText() != "codex idea" {
		t.Fatalf("without a session: jot the first jot labels it, got %q", ss[2].LabelText())
	}

	if two, _ := svc.Sessions(ctx, 2); len(two) != 2 {
		t.Fatalf("n=2: got %d", len(two))
	}
}

// A "session:" jot names its session only if it has a name, which may start on
// the next line; a bare "session:" must not blank out the label.
func TestSessionLabelNeedsAName(t *testing.T) {
	ctx := context.Background()
	svc := sessionService(t)
	jot(t, svc, "claude", "s1", "", "first thought")                // #1
	jot(t, svc, "claude", "s1", "", "session:\nSUP-4821 fix login") // #2
	jot(t, svc, "claude", "s1", "", "session:")                     // #3
	jot(t, svc, "claude", "s1", "", "SESSION:  \n \n")              // #4, trimmed to "SESSION:"
	jot(t, svc, "codex", "s2", "", "codex idea")                    // #5
	jot(t, svc, "codex", "s2", "", "session:")                      // #6

	ss, err := svc.Sessions(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 {
		t.Fatalf("got %d sessions", len(ss))
	}
	if ss[0].ID != "s2" || ss[0].Label.ID != 5 || ss[0].LabelText() != "codex idea" {
		t.Fatalf("bare session: must keep the first jot as label: #%d %q", ss[0].Label.ID, ss[0].LabelText())
	}
	if ss[1].Label.ID != 2 || ss[1].LabelText() != "SUP-4821 fix login" {
		t.Fatalf("name on the next line: #%d %q", ss[1].Label.ID, ss[1].LabelText())
	}
}

func TestFind(t *testing.T) {
	ctx := context.Background()
	svc := sessionService(t)
	jot(t, svc, "claude", "s1", "/work/api", "session: payments retry")  // #1
	jot(t, svc, "claude", "s1", "/work/api", "SUP-4821 needs a ticket")  // #2
	jot(t, svc, "codex", "s2", "/work/web", "unrelated")                 // #3
	jot(t, svc, "claude", "s1", "/work/api", "check sup-4821 TTL again") // #4
	jot(t, svc, "cli", "", "", "SUP-4821 from the terminal")             // #5
	jot(t, svc, "codex", "s3", "/work/web", "SUP-4821 in codex")         // #6

	ss, loose, err := svc.Find(ctx, "SUP-4821")
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 || ss[0].ID != "s3" || ss[1].ID != "s1" {
		t.Fatalf("sessions: %+v", ss)
	}
	s1 := ss[1]
	if len(s1.Matches) != 2 || s1.Matches[0].ID != 2 || s1.Matches[1].ID != 4 {
		t.Fatalf("matches should be the matching jots, oldest first: %+v", s1.Matches)
	}
	if s1.LabelText() != "payments retry" || len(s1.Jots) != 3 {
		t.Fatalf("a found session keeps its full label and jots: %q, %d jots", s1.LabelText(), len(s1.Jots))
	}
	if len(loose) != 1 || loose[0].ID != 5 {
		t.Fatalf("loose: %+v", loose)
	}

	if ss, loose, _ := svc.Find(ctx, "nothing like this"); len(ss) != 0 || len(loose) != 0 {
		t.Fatal("no match should find nothing")
	}
	if _, _, err := svc.Find(ctx, "  "); err == nil {
		t.Fatal("empty query should fail")
	}
}

func TestFindTicketKeyMatchesWholeWord(t *testing.T) {
	ctx := context.Background()
	svc := sessionService(t)
	jot(t, svc, "claude", "s1", "/w", "SUP-4821 token TTL")          // #1
	jot(t, svc, "claude", "s2", "/w", "SUP-48210 is another ticket") // #2
	jot(t, svc, "claude", "s3", "/w", "XSUP-4821 and SUP-4821X")     // #3
	jot(t, svc, "claude", "s4", "/w", "(sup-4821), see PAY-1")       // #4
	jot(t, svc, "cli", "", "", "SUP-4821 from the terminal")         // #5
	jot(t, svc, "claude", "s5", "/w", "UTF-8 handling")              // #6
	jot(t, svc, "claude", "s6", "/w", "the UTF-80 variant")          // #7
	jot(t, svc, "claude", "s7", "/w", "branch SUP-4821_cache")       // #8

	ss, loose, err := svc.Find(ctx, " sup-4821 ")
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 3 || ss[0].ID != "s7" || ss[1].ID != "s4" || ss[2].ID != "s1" || len(loose) != 1 || loose[0].ID != 5 {
		t.Fatalf("whole word: %+v / %+v", ss, loose)
	}
	if ss, _, _ := svc.Find(ctx, "UTF-8"); len(ss) != 1 || ss[0].ID != "s5" {
		t.Fatalf("any key-shaped query is whole word: %+v", ss)
	}
	// Not key-shaped: a plain substring search, as before.
	if ss, _, _ := svc.Find(ctx, "SUP-"); len(ss) != 5 {
		t.Fatalf("substring: %d sessions", len(ss))
	}
	if ss, _, _ := svc.Find(ctx, "sup-4821 token"); len(ss) != 1 {
		t.Fatalf("a phrase is a substring: %+v", ss)
	}
}

func TestSessionsResumeFromStartDir(t *testing.T) {
	svc := sessionService(t)
	ctx := context.Background()
	transcript := filepath.Join(t.TempDir(), "c1.jsonl")
	lines := `{"type":"summary","summary":"no cwd here"}` + "\n" +
		`{"type":"user","cwd":"/work"}` + "\n" +
		`{"type":"user","cwd":"/work/api-wt"}` + "\n"
	if err := os.WriteFile(transcript, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	capture := func(source, session, path, text string) {
		t.Helper()
		req := CaptureRequest{Text: text, Source: source, SessionID: session, Cwd: "/work/api-wt",
			Metadata: map[string]any{"transcript_path": path}}
		if _, err := svc.Capture(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	capture("claude", "c1", transcript, "moved into a worktree")
	capture("claude", "c2", filepath.Join(t.TempDir(), "gone.jsonl"), "transcript deleted")
	capture("codex", "x1", transcript, "codex resumes from anywhere")

	ss, err := svc.Sessions(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range ss {
		got[s.ID] = s.Cwd
	}
	want := map[string]string{"c1": "/work", "c2": "/work/api-wt", "x1": "/work/api-wt"}
	for id, cwd := range want {
		if got[id] != cwd {
			t.Errorf("Sessions %s: cwd %q, want %q", id, got[id], cwd)
		}
	}

	found, _, err := svc.Find(ctx, "worktree")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Cwd != "/work" {
		t.Fatalf("Find: %+v", found)
	}
}

func TestResumeCommandWindows(t *testing.T) {
	old := resumeGOOS
	resumeGOOS = "windows"
	t.Cleanup(func() { resumeGOOS = old })
	s := Session{Source: "claude", ID: "abc-1", Cwd: `C:\Users\me\my repo`}
	if got, want := s.ResumeCommand(), `Set-Location -LiteralPath "C:\Users\me\my repo"; claude --resume abc-1`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	s.Cwd = `C:\repo[1]`
	if got, want := s.ResumeCommand(), `Set-Location -LiteralPath "C:\repo[1]"; claude --resume abc-1`; got != want {
		t.Errorf("brackets: got %q, want %q", got, want)
	}
}

func TestResumeCommand(t *testing.T) {
	old := resumeGOOS
	resumeGOOS = "linux"
	t.Cleanup(func() { resumeGOOS = old })
	cases := []struct {
		s    Session
		want string
	}{
		{Session{Source: "claude", ID: "abc-1", Cwd: "/work/api"}, "cd /work/api && claude --resume abc-1"},
		{Session{Source: "codex", ID: "abc-2", Cwd: "/work/web"}, "codex resume abc-2"},
		{Session{Source: "claude", ID: "abc-3"}, "claude --resume abc-3"},
		{Session{Source: "claude", ID: "abc-4", Cwd: "/Users/me/my repo's"}, `cd '/Users/me/my repo'\''s' && claude --resume abc-4`},
		{Session{Source: "cursor", ID: "abc-5", Cwd: "/work"}, ""},
	}
	for _, c := range cases {
		if got := c.s.ResumeCommand(); got != c.want {
			t.Errorf("%s %s: got %q, want %q", c.s.Source, c.s.ID, got, c.want)
		}
	}
}
