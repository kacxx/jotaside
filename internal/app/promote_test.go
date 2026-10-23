package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kacxx/aside-jot/internal/gitctx"
)

// fakeRunner answers git and gh calls without running anything.
type fakeRunner struct {
	calls   []string // "name arg arg ..."
	stdin   []string // what each call was fed
	remote  string   // git remote get-url output; empty fails
	auth    error    // gh auth status result
	create  error    // gh issue create result
	url     string   // gh issue create output
	view    string   // gh repo view output; empty means a private repo
	viewErr error    // gh repo view result
}

func (f *fakeRunner) Run(_ context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	in := ""
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	f.stdin = append(f.stdin, in)
	switch {
	case name == "git":
		if f.remote == "" {
			return nil, errors.New("exit status 2: error: No such remote 'origin'")
		}
		return []byte(f.remote + "\n"), nil
	case name == "gh" && args[0] == "auth":
		return nil, f.auth
	case name == "gh" && args[0] == "repo":
		if f.viewErr != nil {
			return nil, f.viewErr
		}
		if f.view == "" {
			return []byte(`{"visibility":"PRIVATE"}` + "\n"), nil
		}
		return []byte(f.view + "\n"), nil
	case name == "gh" && args[0] == "issue":
		if f.create != nil {
			return nil, f.create
		}
		return []byte(f.url + "\n"), nil
	}
	return nil, errors.New("unexpected command " + name)
}

func promoteService(t *testing.T, gi gitctx.Info) *Service {
	t.Helper()
	svc, err := Open(filepath.Join(t.TempDir(), "jot.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { svc.Close() })
	svc.git = func(context.Context, string) gitctx.Info { return gi }
	svc.now = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
	if _, err := svc.Capture(context.Background(), CaptureRequest{
		Text: "  fix the cache TTL  \nsee https://example.com/x", Source: "claude", Cwd: "/r",
		Metadata: map[string]any{"transcript_path": "/t.jsonl"},
	}); err != nil {
		t.Fatal(err)
	}
	return svc
}

var repoInfo = gitctx.Info{Root: "/r", Name: "r", Branch: "main", Commit: "abc1234"}

func TestParseRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/kacxx/aside-jot.git":          "kacxx/aside-jot",
		"https://github.com/kacxx/aside-jot":              "kacxx/aside-jot",
		"https://github.com/kacxx/aside-jot/":             "kacxx/aside-jot",
		"https://user:tok@github.com/kacxx/aside-jot.git": "kacxx/aside-jot",
		"http://GitHub.com/kacxx/aside-jot":               "kacxx/aside-jot",
		"git@github.com:kacxx/aside-jot.git":              "kacxx/aside-jot",
		"github.com:kacxx/aside-jot":                      "kacxx/aside-jot",
		"ssh://git@github.com/kacxx/aside-jot.git":        "kacxx/aside-jot",
		"ssh://git@github.com:22/kacxx/aside-jot":         "kacxx/aside-jot",
		"https://ghe.example.com/team/proj.git":           "ghe.example.com/team/proj",
		"git@ghe.example.com:team/proj.git":               "ghe.example.com/team/proj",
	} {
		if got, err := ParseRemote(in); err != nil || got != want {
			t.Errorf("ParseRemote(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"", "/srv/git/proj.git", `C:\src\proj`, "file:///srv/git/proj.git",
		"https://github.com/kacxx", "https://github.com/a/b/c", "git@github.com:", "https://github.com/",
		"https://github.com/-x/y", "git@github.com:o/-n.git",
	} {
		if got, err := ParseRemote(in); err == nil {
			t.Errorf("ParseRemote(%q) = %q; want an error", in, got)
		}
	}
}

func TestParseRemoteRedactsCredentials(t *testing.T) {
	for _, in := range []string{
		"https://kc:ghp_SECRET123@github.com/acme/group/sub/repo.git",
		"https://ghp_SECRET123@github.com/acme",
		"https://kc:ghp_SECRET123@github.com",
		"https://kc:ghp_SECRET123@github.com/a b%zz", // fails url.Parse
		"https://ghp_SECRET123:x@ghe.example.com/a/b/c",
		// A "/" in the secret ends the authority early: url.Parse fails or
		// reads the token as the host.
		"https://x-access-token:ghs_SECRET/CD@github.com/o/n",
		"https://kc:p/SECRET@github.com/o/n.git",
		"https://ghp_SECRET/b@github.com/x",
		"kc:ghp_SECRET@github.com:a/b",
	} {
		got, err := ParseRemote(in)
		if err == nil {
			t.Fatalf("ParseRemote(%q) = %q: want an error", in, got)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("error leaks credentials: %v", err)
		}
	}
	for in, want := range map[string]string{
		"https://u:p@github.com/a/b/c": `"https://github.com/a/b/c"`,
		"https://host/a/b/c":           `"https://host/a/b/c"`,
		"git@github.com:a/b/c":         `"git@github.com:a/b/c"`,
		"/srv/git/proj.git":            `"/srv/git/proj.git"`,
		"https://u:p/w@host/a/b/c":     "not shown",
		"https://host/a@b/c/d":         "not shown",
	} {
		if err := remoteError(in); !strings.Contains(err.Error(), want) {
			t.Errorf("remoteError(%q) = %v, want it to contain %s", in, err, want)
		}
	}
}

func TestIssueTitleAndBody(t *testing.T) {
	e := Entry{
		Text: "  first line  \nsecond line", CreatedAt: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
		Source: "codex", RepoName: "r", Branch: "main", CommitSHA: "abc1234",
	}
	if got := IssueTitle(e); got != "first line" {
		t.Errorf("title: %q", got)
	}
	ts := e.CreatedAt.Local().Format(time.RFC3339)
	if got, want := IssueBody(e), "first line  \nsecond line\n\nCaptured "+ts+" from codex on r@main (abc1234)\n"; got != want {
		t.Errorf("body:\n%q\nwant\n%q", got, want)
	}

	for _, c := range []struct {
		e    Entry
		want string
	}{
		{Entry{Text: "x", CreatedAt: e.CreatedAt}, "Captured " + ts + "\n"},
		{Entry{Text: "x", CreatedAt: e.CreatedAt, Source: "cli", RepoName: "r", CommitSHA: "abc"}, "Captured " + ts + " from cli on r (abc)\n"},
		{Entry{Text: "x", CreatedAt: e.CreatedAt, RepoName: "r", Branch: "b"}, "Captured " + ts + " on r@b\n"},
	} {
		if got := IssueBody(c.e); got != "x\n\n"+c.want {
			t.Errorf("body for %+v: %q", c.e, got)
		}
	}
}

func TestPromoteDryRun(t *testing.T) {
	ctx := context.Background()
	svc := promoteService(t, repoInfo)
	before, _ := svc.Show(ctx, 1)
	f := &fakeRunner{remote: "git@github.com:kacxx/aside-jot.git"}

	p, err := svc.Promote(ctx, f, PromoteRequest{ID: 1, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Repo != "kacxx/aside-jot" || p.Title != "fix the cache TTL" || p.URL != "" ||
		!strings.HasPrefix(p.Body, "fix the cache TTL  \nsee https://example.com/x\n\nCaptured ") {
		t.Fatalf("promotion: %+v", p)
	}
	// Of gh, a dry run only looks up the repo's visibility.
	for _, c := range f.calls {
		if strings.HasPrefix(c, "gh") && c != "gh repo view kacxx/aside-jot --json visibility" {
			t.Errorf("dry run ran %q", c)
		}
	}
	if p.Visibility != "PRIVATE" || p.Warning != "" {
		t.Errorf("visibility %q, warning %q", p.Visibility, p.Warning)
	}

	// With --repo, git doesn't run.
	f = &fakeRunner{}
	if p, err := svc.Promote(ctx, f, PromoteRequest{ID: 1, Repo: "o/n", DryRun: true}); err != nil || p.Repo != "o/n" {
		t.Fatalf("dry run --repo: %+v, %v", p, err)
	}
	if strings.Join(f.calls, "\n") != "gh repo view o/n --json visibility" {
		t.Errorf("dry run --repo ran %v", f.calls)
	}
	if after, _ := svc.Show(ctx, 1); after.Status != before.Status || string(after.Metadata) != string(before.Metadata) {
		t.Fatalf("dry run changed the jot: %+v", after)
	}
}

func TestPromoteSuccessThenRefused(t *testing.T) {
	ctx := context.Background()
	svc := promoteService(t, repoInfo)
	const url = "https://github.com/kacxx/aside-jot/issues/42"
	f := &fakeRunner{remote: "https://github.com/kacxx/aside-jot.git", url: url}

	p, err := svc.Promote(ctx, f, PromoteRequest{ID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != url {
		t.Fatalf("url: %q", p.URL)
	}
	want := []string{
		"git -C /r remote get-url origin",
		"gh auth status --hostname github.com",
		"gh repo view kacxx/aside-jot --json visibility",
		"gh issue create --repo=kacxx/aside-jot --title=fix the cache TTL --body-file=-",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(f.calls, "\n"))
	}
	if f.stdin[3] != p.Body {
		t.Errorf("issue body on stdin: %q", f.stdin[3])
	}

	e, _ := svc.Show(ctx, 1)
	if e.Status != "done" || e.IssueURL != url || !strings.Contains(string(e.Metadata), `"transcript_path":"/t.jsonl"`) {
		t.Fatalf("after promote: %+v (%s)", e, e.Metadata)
	}

	f = &fakeRunner{remote: "https://github.com/kacxx/aside-jot.git", url: "https://other"}
	_, err = svc.Promote(ctx, f, PromoteRequest{ID: 1})
	var ap *AlreadyPromotedError
	if !errors.As(err, &ap) || ap.URL != url || !strings.Contains(err.Error(), url) {
		t.Fatalf("second promote: %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("second promote ran %v", f.calls)
	}
}

func TestPromoteOtherHost(t *testing.T) {
	svc := promoteService(t, repoInfo)
	f := &fakeRunner{remote: "git@ghe.example.com:team/proj.git", url: "https://ghe.example.com/team/proj/issues/1"}
	if _, err := svc.Promote(context.Background(), f, PromoteRequest{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if f.calls[1] != "gh auth status --hostname ghe.example.com" || f.calls[2] != "gh repo view ghe.example.com/team/proj --json visibility" ||
		!strings.Contains(f.calls[3], "--repo=ghe.example.com/team/proj") {
		t.Fatalf("calls: %v", f.calls)
	}
}

func TestPromoteFailuresLeaveJotUnchanged(t *testing.T) {
	ctx := context.Background()
	notFound := &exec.Error{Name: "gh", Err: exec.ErrNotFound}
	for _, c := range []struct {
		name string
		gi   gitctx.Info
		req  PromoteRequest
		f    *fakeRunner
		want string
	}{
		{"no repo", gitctx.Info{}, PromoteRequest{ID: 1}, &fakeRunner{}, "not captured in a git repository; pass --repo"},
		{"no origin", repoInfo, PromoteRequest{ID: 1}, &fakeRunner{}, "no origin remote in /r"},
		{"bad origin", repoInfo, PromoteRequest{ID: 1}, &fakeRunner{remote: "/srv/proj.git"}, "pass --repo"},
		{"bad --repo", repoInfo, PromoteRequest{ID: 1, Repo: "nope"}, &fakeRunner{}, `invalid --repo "nope"`},
		{"--repo looks like a flag", repoInfo, PromoteRequest{ID: 1, Repo: "--help/y"}, &fakeRunner{}, `invalid --repo "--help/y"`},
		{"--repo part looks like a flag", repoInfo, PromoteRequest{ID: 1, Repo: "o/-x"}, &fakeRunner{}, `invalid --repo "o/-x"`},
		{"--repo has an escape", repoInfo, PromoteRequest{ID: 1, Repo: "o/n\x1b[2J"}, &fakeRunner{}, `invalid --repo "o/n\x1b[2J"`},
		{"gh missing", repoInfo, PromoteRequest{ID: 1, Repo: "o/n"}, &fakeRunner{auth: notFound}, "gh (GitHub CLI) not found"},
		{"gh logged out", repoInfo, PromoteRequest{ID: 1, Repo: "o/n"},
			&fakeRunner{auth: errors.New("exit status 1: You are not logged into any GitHub hosts")}, "gh is not logged in to github.com; run 'gh auth login'"},
		{"create fails", repoInfo, PromoteRequest{ID: 1, Repo: "o/n"},
			&fakeRunner{create: errors.New("exit status 1: GraphQL: Could not resolve to a Repository")}, "gh issue create: exit status 1: GraphQL"},
		{"no url", repoInfo, PromoteRequest{ID: 1, Repo: "o/n"}, &fakeRunner{url: "huh"}, "printed no issue URL"},
		{"unknown id", repoInfo, PromoteRequest{ID: 99, Repo: "o/n"}, &fakeRunner{}, ErrNotFound.Error()},
	} {
		t.Run(c.name, func(t *testing.T) {
			svc := promoteService(t, c.gi)
			p, err := svc.Promote(ctx, c.f, c.req)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v; want %q", err, c.want)
			}
			if p.URL != "" {
				t.Errorf("url on failure: %q", p.URL)
			}
			if e, _ := svc.Show(ctx, 1); e.Status != "inbox" || e.IssueURL != "" {
				t.Errorf("jot changed: %+v", e)
			}
		})
	}
	svc := promoteService(t, repoInfo)
	if _, err := svc.Promote(ctx, &fakeRunner{}, PromoteRequest{ID: 99}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

func TestPromotePublicRepo(t *testing.T) {
	ctx := context.Background()
	const publicWarning = "⚠ o/n is PUBLIC: this issue, including the jot and any reply, will be visible to anyone."
	lookupFailed := errors.New("exit status 1: GraphQL: Could not resolve to a Repository with the name 'o/n'.")
	const unknownWarning = "⚠ couldn't check whether o/n is public (exit status 1: GraphQL: Could not resolve to a Repository with the name 'o/n'.): treat this issue, including the jot and any reply, as visible to anyone."
	confirmed := func(shown *Promotion) func(Promotion) error {
		return func(p Promotion) error { *shown = p; return nil }
	}
	for _, c := range []struct {
		name        string
		f           *fakeRunner
		public      bool // PromoteRequest.Public
		confirm     bool // pass a Confirm that accepts
		wantErr     string
		wantVis     string
		wantWarning string
	}{
		{name: "private", f: &fakeRunner{}, wantVis: "PRIVATE"},
		{name: "internal", f: &fakeRunner{view: `{"visibility":"INTERNAL"}`}, wantVis: "INTERNAL"},
		{name: "private, lower case", f: &fakeRunner{view: `{"visibility":"private"}`}, wantVis: "PRIVATE"},
		{name: "public, unconfirmed", f: &fakeRunner{view: `{"visibility":"PUBLIC"}`},
			wantErr: "o/n is PUBLIC, so the issue, including the jot and any reply, would be visible to anyone; pass --public with --yes"},
		{name: "public, unconfirmed, Public", f: &fakeRunner{view: `{"visibility":"PUBLIC"}`}, public: true,
			wantVis: "PUBLIC", wantWarning: publicWarning},
		{name: "public, confirmed", f: &fakeRunner{view: `{"visibility":"PUBLIC"}`}, confirm: true,
			wantVis: "PUBLIC", wantWarning: publicWarning},
		{name: "lookup fails, unconfirmed", f: &fakeRunner{viewErr: lookupFailed},
			wantErr: "couldn't check whether o/n is public (exit status 1: GraphQL: Could not resolve"},
		{name: "lookup fails, unconfirmed, Public", f: &fakeRunner{viewErr: lookupFailed}, public: true, wantWarning: unknownWarning},
		{name: "lookup fails, confirmed", f: &fakeRunner{viewErr: lookupFailed}, confirm: true, wantWarning: unknownWarning},
		{name: "not JSON", f: &fakeRunner{view: "huh"},
			wantErr: "couldn't check whether o/n is public (gh repo view printed no visibility); pass --public with --yes"},
		{name: "unknown visibility", f: &fakeRunner{view: `{"visibility":"SECRET"}`},
			wantErr: `couldn't check whether o/n is public (gh repo view printed visibility "SECRET")`},
		{name: "no visibility", f: &fakeRunner{view: `{}`}, wantErr: `couldn't check whether o/n is public`},
	} {
		t.Run(c.name, func(t *testing.T) {
			svc := promoteService(t, repoInfo)
			c.f.url = "https://github.com/o/n/issues/1"
			req := PromoteRequest{ID: 1, Repo: "o/n", Public: c.public}
			var shown Promotion
			if c.confirm {
				req.Confirm = confirmed(&shown)
			}
			p, err := svc.Promote(ctx, c.f, req)
			created := strings.HasPrefix(c.f.calls[len(c.f.calls)-1], "gh issue create")
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v; want %q", err, c.wantErr)
				}
				if created {
					t.Fatal("created an issue")
				}
				if e, _ := svc.Show(ctx, 1); e.Status != "inbox" || e.IssueURL != "" {
					t.Errorf("jot changed: %+v", e)
				}
				return
			}
			if err != nil || !created || p.URL == "" {
				t.Fatalf("promote: %+v, %v (calls %v)", p, err, c.f.calls)
			}
			if p.Visibility != c.wantVis || p.Warning != c.wantWarning {
				t.Errorf("visibility %q, warning %q; want %q, %q", p.Visibility, p.Warning, c.wantVis, c.wantWarning)
			}
			if c.confirm && shown.Warning != c.wantWarning {
				t.Errorf("Confirm was shown warning %q; want %q", shown.Warning, c.wantWarning)
			}
		})
	}
}

func TestPromoteDryRunWarnsAboutPublicRepo(t *testing.T) {
	ctx := context.Background()
	notFound := &exec.Error{Name: "gh", Err: exec.ErrNotFound}
	for _, c := range []struct {
		f           *fakeRunner
		wantWarning string
	}{
		{&fakeRunner{}, ""},
		{&fakeRunner{view: `{"visibility":"PUBLIC"}`}, "⚠ o/n is PUBLIC: this issue, including the jot and any reply, will be visible to anyone."},
		{&fakeRunner{viewErr: notFound}, "⚠ couldn't check whether o/n is public (gh (GitHub CLI) not found on PATH; install it"},
		// gh's message is shown on one line, without escapes.
		{&fakeRunner{viewErr: errors.New("exit status 1: bad\n\x1b[2Jthing")}, "⚠ couldn't check whether o/n is public (exit status 1: bad thing): treat"},
	} {
		svc := promoteService(t, repoInfo)
		p, err := svc.Promote(ctx, c.f, PromoteRequest{ID: 1, Repo: "o/n", DryRun: true})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(p.Warning, c.wantWarning) || (c.wantWarning == "") != (p.Warning == "") {
			t.Errorf("warning %q; want %q", p.Warning, c.wantWarning)
		}
		if strings.Join(c.f.calls, "\n") != "gh repo view o/n --json visibility" {
			t.Errorf("dry run ran %v", c.f.calls)
		}
	}
}

// replyTranscript mimics a real Claude Code transcript: every content block is
// its own record, a message is split across records sharing a message id, tool
// results come back as user records, and a sidechain and a record from after the jot
// (captured at 12:00:00Z) sit among the turn it belongs to.
const replyTranscript = `{"type":"user","timestamp":"2026-09-30T11:00:00Z","message":{"role":"user","content":"look at sessions"}}
{"type":"assistant","timestamp":"2026-09-30T11:00:05Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"early reply"}]}}
{"type":"user","timestamp":"2026-09-30T11:58:00Z","message":{"role":"user","content":[{"type":"text","text":"why is resume cd wrong?"}]}}
{"type":"assistant","timestamp":"2026-09-30T11:58:05Z","message":{"id":"m2","role":"assistant","content":[{"type":"text","text":"Let me check."}]}}
{"type":"assistant","timestamp":"2026-09-30T11:58:06Z","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash"}]}}
{"type":"user","timestamp":"2026-09-30T11:58:10Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}
{"type":"assistant","timestamp":"2026-09-30T11:59:00Z","message":{"id":"m3","role":"assistant","content":[{"type":"text","text":"Found it: resume cds into the wrong folder."}]}}
{"type":"assistant","timestamp":"2026-09-30T11:59:01Z","message":{"id":"m3","role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Read"}]}}
{"type":"user","timestamp":"2026-09-30T11:59:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"ok"}]}}
{"type":"assistant","timestamp":"2026-09-30T11:59:30Z","isSidechain":true,"message":{"id":"s1","role":"assistant","content":[{"type":"text","text":"subagent chatter"}]}}
{"type":"assistant","timestamp":"2026-09-30T12:05:00Z","message":{"id":"m5","role":"assistant","content":[{"type":"text","text":"later reply"}]}}
{"type":"assistant","timestamp":"2026-09-30T11:59:40Z","message":{"id":"m4","role":"assistant","content":[{"type":"text","text":"Evidence: cwd moved."}]}}
`

func TestPromoteWithReply(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	svc := promoteService(t, repoInfo)
	if _, err := svc.Capture(ctx, CaptureRequest{Text: "resume uses wrong folder", Source: "claude",
		Metadata: map[string]any{"transcript_path": path}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(replyTranscript), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{remote: "git@github.com:kacxx/aside-jot.git"}

	p, err := svc.Promote(ctx, f, PromoteRequest{ID: 2, DryRun: true, WithReply: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "resume uses wrong folder\n\n## Agent reply\n\nLet me check.\n\nFound it: resume cds into the wrong folder.\n\nEvidence: cwd moved.\n\nCaptured "
	if !strings.HasPrefix(p.Body, want) {
		t.Fatalf("body:\n%s", p.Body)
	}
	for _, bad := range []string{"early reply", "later reply", "subagent", "tool_use"} {
		if strings.Contains(p.Body, bad) {
			t.Errorf("body has %q", bad)
		}
	}

	// Without the flag the body is unchanged.
	p, err = svc.Promote(ctx, f, PromoteRequest{ID: 2, DryRun: true})
	if err != nil || strings.Contains(p.Body, "Agent reply") {
		t.Fatalf("no flag: %q, %v", p.Body, err)
	}
}

func TestPromoteRemovesTerminalEscapes(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	svc := promoteService(t, repoInfo)
	if _, err := svc.Capture(ctx, CaptureRequest{Text: "fix \x1b]0;pwned\x07the\rbug\nwith\tdetails", Source: "claude",
		Metadata: map[string]any{"transcript_path": path}}); err != nil {
		t.Fatal(err)
	}
	reply := "visible\x1b[2K\r\x1b[1Ahidden\nsee \x1b]8;;https://evil.example\x1b\\the docs\x1b]8;;\x1b\\\n\tindented"
	rec, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": "2026-09-30T11:59:00Z",
		"message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": reply}}}})
	if err := os.WriteFile(path, append(rec, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	const wantTitle = "fix the bug"
	const wantBody = "fix the bug\nwith\tdetails\n\n## Agent reply\n\nvisible hidden\nsee the docs\n\tindented\n\nCaptured "

	f := &fakeRunner{remote: "git@github.com:o/n.git", url: "https://github.com/o/n/issues/1"}
	p, err := svc.Promote(ctx, f, PromoteRequest{ID: 2, DryRun: true, WithReply: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != wantTitle || !strings.HasPrefix(p.Body, wantBody) {
		t.Fatalf("dry run:\ntitle %q\nbody  %q", p.Title, p.Body)
	}

	// What Confirm is shown is what gh is given, and neither has escapes.
	var shown Promotion
	p, err = svc.Promote(ctx, f, PromoteRequest{ID: 2, WithReply: true, Confirm: func(p Promotion) error { shown = p; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	posted := f.stdin[len(f.stdin)-1]
	if shown.Title != wantTitle || !strings.HasPrefix(shown.Body, wantBody) || posted != shown.Body ||
		!strings.Contains(f.calls[len(f.calls)-1], "--title="+wantTitle+" ") {
		t.Fatalf("shown %+v\nposted %q\ncall %q", shown, posted, f.calls[len(f.calls)-1])
	}
	if strings.ContainsAny(posted, "\x1b\r\a") {
		t.Fatalf("posted body has control characters: %q", posted)
	}
}

func TestPromoteWithReplyErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, []byte(`{"type":"user","timestamp":"2026-09-30T11:00:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, source string
		meta         map[string]any
		want         string
	}{
		{"no transcript path", "claude", nil, "no transcript path"},
		{"missing file", "claude", map[string]any{"transcript_path": filepath.Join(dir, "gone.jsonl")}, "cannot read transcript"},
		{"no reply", "claude", map[string]any{"transcript_path": empty}, "no agent reply found"},
		{"codex", "codex", map[string]any{"transcript_path": empty}, "Claude Code jots only"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := promoteService(t, repoInfo)
			if _, err := svc.Capture(ctx, CaptureRequest{Text: "x", Source: c.source, Metadata: c.meta}); err != nil {
				t.Fatal(err)
			}
			f := &fakeRunner{remote: "https://github.com/o/n.git", url: "https://github.com/o/n/issues/1"}
			_, err := svc.Promote(ctx, f, PromoteRequest{ID: 2, WithReply: true})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			for _, call := range f.calls {
				if strings.HasPrefix(call, "gh issue") {
					t.Errorf("created an issue: %q", call)
				}
			}
		})
	}
}

func TestTruncateReply(t *testing.T) {
	long := strings.Repeat("é", maxReplyRunes+10)
	got := truncateReply(long)
	if !strings.Contains(got, "reply cut") || !strings.HasPrefix(got, strings.Repeat("é", maxReplyRunes)) {
		t.Fatalf("truncate: %.60q", got)
	}
	if truncateReply("short") != "short" {
		t.Fatal("short reply changed")
	}
}
