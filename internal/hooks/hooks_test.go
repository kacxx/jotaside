package hooks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kacxx/jotaside/internal/app"
	"github.com/kacxx/jotaside/internal/gitctx"
	"github.com/kacxx/jotaside/internal/store"
)

// TestMain raises the git time budget: these tests check the git fields of a
// capture, and four git runs can overrun 750 ms on a shared Windows runner.
func TestMain(m *testing.M) {
	gitctx.Timeout = 30 * time.Second
	os.Exit(m.Run())
}

// fixture loads testdata/hooks/<tool>/<name>.json, optionally rewriting
// top-level fields (e.g. cwd) so captures point at real temp dirs.
func fixture(t *testing.T, tool, name string, override map[string]any) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "hooks", tool, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(override) == 0 {
		return b
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range override {
		m[k] = v
	}
	b, err = json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// neverOpen fails the test if the hook tries to open the database.
func neverOpen(t *testing.T) app.Opener {
	return func() (*app.Service, error) {
		t.Helper()
		t.Error("opener called for a non-capture prompt")
		return nil, errors.New("must not open")
	}
}

// tempDB returns an opener for a fresh database and its path.
func tempDB(t *testing.T) (app.Opener, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jot.db")
	return func() (*app.Service, error) { return app.Open(path, time.Second) }, path
}

func entries(t *testing.T, path string) []app.Entry {
	t.Helper()
	svc, err := app.Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	es, err := svc.Inbox(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return es
}

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "feature/jot")
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func runClaude(t *testing.T, payload []byte, open app.Opener) (string, *ClaudeBlock) {
	t.Helper()
	var out bytes.Buffer
	err := Claude(context.Background(), bytes.NewReader(payload), &out, open)
	var exit *BlockExit
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		if exit != nil {
			t.Fatalf("BlockExit %q with no JSON", exit.Reason)
		}
		return "", nil
	}
	var b ClaudeBlock
	if err := json.Unmarshal(out.Bytes(), &b); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	if exit == nil || exit.Reason != b.Reason {
		t.Fatalf("a block must return BlockExit with its reason %q; got %v", b.Reason, err)
	}
	return out.String(), &b
}

func runCursor(t *testing.T, payload []byte, open app.Opener) (string, CursorOutput) {
	t.Helper()
	var out bytes.Buffer
	if err := Cursor(context.Background(), bytes.NewReader(payload), &out, open); err != nil {
		t.Fatal(err)
	}
	var o CursorOutput
	if err := json.Unmarshal(out.Bytes(), &o); err != nil {
		t.Fatalf("invalid JSON %q: %v", out.String(), err)
	}
	return out.String(), o
}

func assertBlockedOK(t *testing.T, b *ClaudeBlock, id int64) {
	t.Helper()
	if b == nil || b.Decision != "block" {
		t.Fatalf("expected block, got %+v", b)
	}
	if h := b.HookSpecificOutput; h.HookEventName != "UserPromptSubmit" || !h.SuppressOriginalPrompt {
		t.Fatalf("hookSpecificOutput = %+v, want UserPromptSubmit with suppressOriginalPrompt", h)
	}
	if want := "✓ Jotted #" + itoa(id); b.Reason != want {
		t.Fatalf("reason = %q, want %q", b.Reason, want)
	}
}

func assertBlockedFailed(t *testing.T, reason, prompt string) {
	t.Helper()
	if !strings.Contains(reason, "NOT saved") || !strings.Contains(reason, prompt) {
		t.Fatalf("failure message must say NOT saved and include %q; got %q", prompt, reason)
	}
}

func itoa(i int64) string { b, _ := json.Marshal(i); return string(b) }

// --- Claude ---

func TestClaudePassThrough(t *testing.T) {
	for _, name := range []string{"normal", "mid_prompt", "malformed"} {
		t.Run(name, func(t *testing.T) {
			out, _ := runClaude(t, fixture(t, "claude", name, nil), neverOpen(t))
			if out != "" {
				t.Fatalf("pass-through must write nothing, got %q", out)
			}
		})
	}
	t.Run("empty stdin", func(t *testing.T) {
		if out, _ := runClaude(t, nil, neverOpen(t)); out != "" {
			t.Fatalf("got %q", out)
		}
	})
	t.Run("wrong prompt type", func(t *testing.T) {
		if out, _ := runClaude(t, []byte(`{"prompt": 42}`), neverOpen(t)); out != "" {
			t.Fatalf("got %q", out)
		}
	})
}

func TestClaudeCaptureInRepo(t *testing.T) {
	repo := gitRepo(t)
	open, path := tempDB(t)
	_, b := runClaude(t, fixture(t, "claude", "capture", map[string]any{"cwd": repo}), open)
	assertBlockedOK(t, b, 1)

	es := entries(t, path)
	if len(es) != 1 {
		t.Fatalf("entries: %d", len(es))
	}
	e := es[0]
	if e.Text != "token cache TTL looks too long, check with infra before shipping" ||
		e.Source != "claude" || e.SessionID != "3f1c2a9e-7b4d-4e0a-9c55-0d2f8e6b1a47" ||
		e.Cwd != repo || e.RepoName != filepath.Base(repo) || e.RepoRoot == "" ||
		e.Branch != "feature/jot" || e.CommitSHA != git(t, repo, "rev-parse", "--short", "HEAD") {
		t.Fatalf("entry: %+v", e)
	}
	if !strings.Contains(string(e.Metadata), "transcript_path") {
		t.Fatalf("metadata: %s", e.Metadata)
	}

	// A second jot gets the next id.
	_, b = runClaude(t, fixture(t, "claude", "capture", map[string]any{"cwd": repo, "prompt": ">> and the refresh token TTL"}), open)
	assertBlockedOK(t, b, 2)
}

// TestClaudeBlockJSON checks the exact keys Claude Code reads, which a decode
// into ClaudeBlock can't: json.Unmarshal matches keys case-insensitively.
func TestClaudeBlockJSON(t *testing.T) {
	const prompt = ">> token cache TTL looks too long, check with infra before shipping"
	ok, _ := tempDB(t)
	for name, c := range map[string]struct {
		open  app.Opener
		check func(t *testing.T, reason string)
	}{
		"saved": {ok, func(t *testing.T, reason string) {
			if reason != "✓ Jotted #1" {
				t.Fatalf("reason = %q", reason)
			}
		}},
		"not saved": {
			func() (*app.Service, error) { return nil, errors.New("disk on fire") },
			func(t *testing.T, reason string) { assertBlockedFailed(t, reason, prompt) },
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, _ := runClaude(t, fixture(t, "claude", "capture", map[string]any{"cwd": t.TempDir()}), c.open)
			var got map[string]any
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			reason, _ := got["reason"].(string)
			c.check(t, reason)
			want := map[string]any{
				"decision": "block",
				"reason":   reason,
				"hookSpecificOutput": map[string]any{
					"hookEventName":          "UserPromptSubmit",
					"suppressOriginalPrompt": true,
				},
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("output = %s\nwant keys and values %v", out, want)
			}
		})
	}
}

func TestClaudeCaptureDesktopEnv(t *testing.T) {
	const id = "local_7d6831ca-0cab-461d-8d71-d3f9f9aeaf3c"
	cases := []struct {
		name, hostID, entrypoint string
		want                     map[string]any
	}{
		{"desktop", id, "claude-desktop", map[string]any{
			store.MetaDesktopSession: id, store.MetaClaudeEntrypoint: "claude-desktop"}},
		{"unset", "", "", map[string]any{}},
		{"cli", "", "cli", map[string]any{store.MetaClaudeEntrypoint: "cli"}},
		{"bare uuid", "7d6831ca-0cab-461d-8d71-d3f9f9aeaf3c", "", map[string]any{}},
		{"uppercase", "local_7D6831CA-0CAB-461D-8D71-D3F9F9AEAF3C", "", map[string]any{}},
		{"injection", id + "&q=x", "claude desktop\n", map[string]any{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(envHostSessionID, c.hostID)
			t.Setenv(envEntrypoint, c.entrypoint)
			open, path := tempDB(t)
			_, b := runClaude(t, fixture(t, "claude", "capture", map[string]any{"cwd": t.TempDir()}), open)
			assertBlockedOK(t, b, 1)

			var meta map[string]any
			if err := json.Unmarshal(entries(t, path)[0].Metadata, &meta); err != nil {
				t.Fatal(err)
			}
			if meta[store.MetaTranscriptPath] == nil {
				t.Fatalf("transcript_path missing: %v", meta)
			}
			delete(meta, store.MetaTranscriptPath)
			if len(meta) != len(c.want) {
				t.Fatalf("metadata = %v, want %v", meta, c.want)
			}
			for k, v := range c.want {
				if meta[k] != v {
					t.Fatalf("metadata[%s] = %v, want %v", k, meta[k], v)
				}
			}
		})
	}
}

func TestClaudeCaptureDetachedHEAD(t *testing.T) {
	repo := gitRepo(t)
	git(t, repo, "checkout", "-q", "--detach")
	open, path := tempDB(t)
	_, b := runClaude(t, fixture(t, "claude", "capture", map[string]any{"cwd": repo}), open)
	assertBlockedOK(t, b, 1)
	e := entries(t, path)[0]
	if e.Branch != "" || e.CommitSHA == "" || e.RepoRoot == "" {
		t.Fatalf("detached: %+v", e)
	}
}

func TestClaudeCaptureNonGitDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	open, path := tempDB(t)
	_, b := runClaude(t, fixture(t, "claude", "capture", map[string]any{"cwd": dir}), open)
	assertBlockedOK(t, b, 1)
	e := entries(t, path)[0]
	if e.Cwd != dir || e.RepoRoot != "" || e.RepoName != "" || e.Branch != "" || e.CommitSHA != "" {
		t.Fatalf("non-git: %+v", e)
	}
}

func TestClaudeDBOpenFailure(t *testing.T) {
	// A regular file where the data directory should be.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, open := range map[string]app.Opener{
		"opener error": func() (*app.Service, error) { return nil, errors.New("disk on fire") },
		"real open":    func() (*app.Service, error) { return app.Open(filepath.Join(blocker, "jot.db"), time.Second) },
		"panic":        func() (*app.Service, error) { panic("boom") },
	} {
		t.Run(name, func(t *testing.T) {
			payload := fixture(t, "claude", "capture", nil)
			_, b := runClaude(t, payload, open)
			if b == nil || b.Decision != "block" {
				t.Fatalf("a recognised jot must be blocked even on failure: %+v", b)
			}
			assertBlockedFailed(t, b.Reason, ">> token cache TTL looks too long, check with infra before shipping")
		})
	}
}

// A panic after the prompt is recognised as a jot still blocks it as NOT saved.
func TestPanicAfterMatchBlocks(t *testing.T) {
	for _, exitBlock := range []bool{true, false} {
		h := promptHook{
			event:     "UserPromptSubmit",
			block:     func(reason string) any { return ClaudeBlock{Decision: "block", Reason: reason} },
			exitBlock: exitBlock,
			parse: func([]byte) (string, string, func() app.CaptureRequest, error) {
				return "UserPromptSubmit", ">> keep me", func() app.CaptureRequest { panic("boom") }, nil
			},
		}
		var out bytes.Buffer
		err := h.run(context.Background(), strings.NewReader(`{}`), &out, neverOpen(t))
		var exit *BlockExit
		if exitBlock != errors.As(err, &exit) {
			t.Fatalf("exitBlock=%v: err = %v", exitBlock, err)
		}
		var b ClaudeBlock
		if err := json.Unmarshal(out.Bytes(), &b); err != nil {
			t.Fatalf("invalid JSON %q: %v", out.String(), err)
		}
		if b.Decision != "block" || !strings.Contains(b.Reason, "internal error: boom") {
			t.Fatalf("exitBlock=%v: %+v", exitBlock, b)
		}
		assertBlockedFailed(t, b.Reason, ">> keep me")
	}
}

// lockDB holds an exclusive lock on the database from another connection.
func lockDB(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", store.DSN(path, 0))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.ExecContext(context.Background(), "ROLLBACK")
		conn.Close()
		db.Close()
	})
}

func TestClaudeDBLocked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jot.db")
	// Create the schema first so the lock is on a real database.
	svc, err := app.Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	svc.Close()
	lockDB(t, path)

	open := func() (*app.Service, error) { return app.Open(path, 100*time.Millisecond) }
	start := time.Now()
	_, b := runClaude(t, fixture(t, "claude", "capture", map[string]any{"cwd": t.TempDir()}), open)
	if b == nil || b.Decision != "block" {
		t.Fatalf("locked DB must still block: %+v", b)
	}
	assertBlockedFailed(t, b.Reason, ">> token cache TTL")
	if !strings.Contains(strings.ToLower(b.Reason), "locked") && !strings.Contains(strings.ToLower(b.Reason), "busy") {
		t.Errorf("reason should mention the lock: %q", b.Reason)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("locked capture took %v; busy_timeout not honoured", d)
	}
}

// --- Cursor ---

func TestCursorPassThrough(t *testing.T) {
	for _, name := range []string{"normal", "malformed"} {
		t.Run(name, func(t *testing.T) {
			out, o := runCursor(t, fixture(t, "cursor", name, nil), neverOpen(t))
			if !o.Continue || strings.TrimSpace(out) != `{"continue":true}` {
				t.Fatalf("pass-through must be {\"continue\":true}, got %q", out)
			}
		})
	}
	t.Run("mid-prompt", func(t *testing.T) {
		_, o := runCursor(t, fixture(t, "cursor", "normal", map[string]any{"prompt": "use >> to append"}), neverOpen(t))
		if !o.Continue {
			t.Fatal("should continue")
		}
	})
}

func TestCursorSingleRoot(t *testing.T) {
	repo := gitRepo(t)
	open, path := tempDB(t)
	_, o := runCursor(t, fixture(t, "cursor", "capture_single_root", map[string]any{"workspace_roots": []string{repo}}), open)
	if o.Continue || o.UserMessage != "✓ Jotted #1" {
		t.Fatalf("got %+v", o)
	}
	e := entries(t, path)[0]
	if e.Source != "cursor" || e.SessionID != "e5b0a1c7-3d2f-4c8e-9a14-7f6d2b0c8e31" ||
		e.Cwd != repo || e.Branch != "feature/jot" || e.CommitSHA == "" ||
		e.Text != "orders pagination should probably be cursor-based, not offset" {
		t.Fatalf("entry: %+v", e)
	}
	if strings.Contains(string(e.Metadata), "workspace_roots") {
		t.Fatalf("single root should not be in metadata: %s", e.Metadata)
	}
}

// A wrong-typed element in workspace_roots must not be saved as an empty root.
func TestCursorWrongTypedRootDropped(t *testing.T) {
	repo := gitRepo(t)
	open, path := tempDB(t)
	_, o := runCursor(t, fixture(t, "cursor", "capture_single_root", map[string]any{
		"workspace_roots": []any{repo, 5}}), open)
	if o.Continue {
		t.Fatalf("got %+v", o)
	}
	e := entries(t, path)[0]
	if e.Cwd != repo || strings.Contains(string(e.Metadata), "workspace_roots") {
		t.Fatalf("one real root must be the cwd, with no empty root recorded: cwd=%q meta=%s", e.Cwd, e.Metadata)
	}

	open, path = tempDB(t)
	runCursor(t, fixture(t, "cursor", "capture_single_root", map[string]any{
		"workspace_roots": []any{"/repo", 5, "/other"}}), open)
	var meta struct {
		Roots []string `json:"workspace_roots"`
	}
	e = entries(t, path)[0]
	if err := json.Unmarshal(e.Metadata, &meta); err != nil || len(meta.Roots) != 2 || meta.Roots[0] != "/repo" || meta.Roots[1] != "/other" {
		t.Fatalf("roots = %v (%v), want [/repo /other]", meta.Roots, err)
	}
}

func TestCursorMultiRoot(t *testing.T) {
	a, b := gitRepo(t), gitRepo(t)
	open, path := tempDB(t)
	_, o := runCursor(t, fixture(t, "cursor", "capture_multi_root", map[string]any{"workspace_roots": []string{a, b}}), open)
	if o.Continue || o.UserMessage != "✓ Jotted #1" {
		t.Fatalf("got %+v", o)
	}
	e := entries(t, path)[0]
	if e.Cwd != "" || e.RepoRoot != "" || e.Branch != "" {
		t.Fatalf("multi-root must not guess a cwd: %+v", e)
	}
	var meta struct {
		Roots []string `json:"workspace_roots"`
	}
	if err := json.Unmarshal(e.Metadata, &meta); err != nil || len(meta.Roots) != 2 || meta.Roots[0] != a || meta.Roots[1] != b {
		t.Fatalf("metadata: %s (%v)", e.Metadata, err)
	}
}

func TestCursorFailureBlocks(t *testing.T) {
	_, o := runCursor(t, fixture(t, "cursor", "capture_single_root", nil),
		func() (*app.Service, error) { return nil, errors.New("no db") })
	if o.Continue {
		t.Fatal("a recognised jot must be blocked even on failure")
	}
	assertBlockedFailed(t, o.UserMessage, ">> orders pagination should probably be cursor-based, not offset")
}

// A field of the wrong JSON type must not let a jot through: the payload is
// still well-formed, the prompt is still a jot, and it is saved with the
// metadata that did decode.
func TestWrongTypedFieldsStillBlock(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		for name, override := range map[string]map[string]any{
			"session_id":      {"session_id": 123},
			"cwd":             {"cwd": []int{1}},
			"transcript_path": {"transcript_path": false},
			"hook_event_name": {"hook_event_name": 5},
		} {
			t.Run(name, func(t *testing.T) {
				open, path := tempDB(t)
				_, b := runClaude(t, fixture(t, "claude", "capture", override), open)
				assertBlockedOK(t, b, 1)
				e := entries(t, path)[0]
				if e.Text != "token cache TTL looks too long, check with infra before shipping" || e.Source != "claude" {
					t.Fatalf("entry: %+v", e)
				}
				if name != "session_id" && e.SessionID != "3f1c2a9e-7b4d-4e0a-9c55-0d2f8e6b1a47" {
					t.Fatalf("well-typed session_id lost: %+v", e)
				}
			})
		}
	})
	t.Run("cursor", func(t *testing.T) {
		for name, override := range map[string]map[string]any{
			"conversation_id":        {"conversation_id": 7},
			"generation_id":          {"generation_id": []string{"x"}},
			"workspace_roots string": {"workspace_roots": "/x"},
			"workspace_roots ints":   {"workspace_roots": []int{1}},
			"hook_event_name":        {"hook_event_name": true},
		} {
			t.Run(name, func(t *testing.T) {
				open, path := tempDB(t)
				_, o := runCursor(t, fixture(t, "cursor", "capture_single_root", override), open)
				if o.Continue || o.UserMessage != "✓ Jotted #1" {
					t.Fatalf("got %+v", o)
				}
				if e := entries(t, path)[0]; e.Source != "cursor" || e.Text != "orders pagination should probably be cursor-based, not offset" {
					t.Fatalf("entry: %+v", e)
				}
			})
		}
	})
	t.Run("prompt itself wrong type passes through", func(t *testing.T) {
		if out, _ := runClaude(t, []byte(`{"session_id":1,"prompt":[">> x"]}`), neverOpen(t)); out != "" {
			t.Fatalf("got %q", out)
		}
		if _, o := runCursor(t, []byte(`{"workspace_roots":"/x","prompt":{"a":">> x"}}`), neverOpen(t)); !o.Continue {
			t.Fatalf("got %+v", o)
		}
	})
}

// --- Claude hook imported by Cursor ---

// runClaudeAsCursor runs `jotaside hook claude` on a Cursor payload, as Cursor
// does when it imports the hook from ~/.claude/settings.json.
func runClaudeAsCursor(t *testing.T, payload []byte, open app.Opener) (string, CursorOutput) {
	t.Helper()
	var out bytes.Buffer
	if err := Claude(context.Background(), bytes.NewReader(payload), &out, open); err != nil {
		t.Fatal(err)
	}
	var o CursorOutput
	if err := json.Unmarshal(out.Bytes(), &o); err != nil {
		t.Fatalf("Cursor needs a reply, got %q: %v", out.String(), err)
	}
	return out.String(), o
}

func TestClaudeOnCursorEventPassThrough(t *testing.T) {
	out, o := runClaudeAsCursor(t, fixture(t, "cursor", "normal", nil), neverOpen(t))
	if !o.Continue || strings.TrimSpace(out) != `{"continue":true}` {
		t.Fatalf("pass-through must be {\"continue\":true}, got %q", out)
	}
	// A malformed payload names no event, so it stays silent as a Claude hook.
	if out, _ := runClaude(t, fixture(t, "cursor", "malformed", nil), neverOpen(t)); out != "" {
		t.Fatalf("malformed: got %q", out)
	}
}

// Issue #23: a silent Claude hook made Cursor submit a jot that `jotaside hook
// cursor` had blocked. On beforeSubmitPrompt it must block in Cursor format.
func TestClaudeOnCursorEventBlocks(t *testing.T) {
	t.Setenv(envHostSessionID, "local_7d6831ca-0cab-461d-8d71-d3f9f9aeaf3c")
	t.Setenv(envEntrypoint, "claude-desktop")
	repo := gitRepo(t)
	open, path := tempDB(t)
	_, o := runClaudeAsCursor(t, fixture(t, "cursor", "capture_single_root", map[string]any{"workspace_roots": []string{repo}}), open)
	if o.Continue || o.UserMessage != "✓ Jotted #1" {
		t.Fatalf("got %+v", o)
	}
	e := entries(t, path)[0]
	if e.Source != "cursor" || e.SessionID != "e5b0a1c7-3d2f-4c8e-9a14-7f6d2b0c8e31" || e.Cwd != repo {
		t.Fatalf("entry: %+v", e)
	}
	// A Cursor jot never records Claude Desktop's environment.
	for _, k := range []string{store.MetaDesktopSession, store.MetaClaudeEntrypoint} {
		if strings.Contains(string(e.Metadata), k) {
			t.Fatalf("cursor jot has %s: %s", k, e.Metadata)
		}
	}
}

func TestClaudeOnCursorEventFailureBlocks(t *testing.T) {
	failing := func() (*app.Service, error) { return nil, errors.New("disk on fire") }
	payload := fixture(t, "cursor", "capture_single_root", nil)
	_, o := runClaudeAsCursor(t, payload, failing)
	if o.Continue {
		t.Fatalf("got %+v", o)
	}
	assertBlockedFailed(t, o.UserMessage, "orders pagination")
}

// With both hooks installed Cursor runs each on the same prompt; the jot is
// saved once and both block it. Cursor joins their user_message values, so
// only the hook that saved it confirms.
func TestCursorAndClaudeHooksSaveOnce(t *testing.T) {
	open, path := tempDB(t)
	payload := fixture(t, "cursor", "capture_single_root", nil)
	_, a := runCursor(t, payload, open)
	out, b := runClaudeAsCursor(t, payload, open)
	if a.Continue || a.UserMessage != "✓ Jotted #1" {
		t.Fatalf("first hook: %+v", a)
	}
	if b.Continue || strings.TrimSpace(out) != `{"continue":false}` {
		t.Fatalf("second hook must block silently, got %q", out)
	}
	if es := entries(t, path); len(es) != 1 {
		t.Fatalf("saved %d entries, want 1", len(es))
	}

	// A new prompt in the same conversation has a new generation id.
	_, c := runCursor(t, fixture(t, "cursor", "capture_single_root", map[string]any{"generation_id": "g2", "prompt": ">> and the page size"}), open)
	if c.UserMessage != "✓ Jotted #2" {
		t.Fatalf("next prompt: %+v", c)
	}
}

// Cursor may run the two hooks at the same time.
func TestCursorAndClaudeHooksSaveOnceConcurrently(t *testing.T) {
	open, path := tempDB(t)
	// Create the database first so both hooks race on the insert, not the migration.
	if svc, err := open(); err != nil {
		t.Fatal(err)
	} else {
		svc.Close()
	}
	payload := fixture(t, "cursor", "capture_single_root", nil)
	const n = 8
	replies := make(chan string, n)
	for i := range n {
		hook := Cursor
		if i%2 == 1 {
			hook = Claude
		}
		go func() {
			var out bytes.Buffer
			_ = hook(context.Background(), bytes.NewReader(payload), &out, open)
			replies <- strings.TrimSpace(out.String())
		}()
	}
	confirmed := 0
	for range n {
		switch r := <-replies; r {
		case `{"continue":false,"user_message":"✓ Jotted #1"}`:
			confirmed++
		case `{"continue":false}`:
		default:
			t.Errorf("reply %q", r)
		}
	}
	if confirmed != 1 {
		t.Errorf("%d hooks confirmed, want 1", confirmed)
	}
	if es := entries(t, path); len(es) != 1 {
		t.Fatalf("saved %d entries, want 1", len(es))
	}
}

// Without a generation id the prompt can't be matched, but the same jot in
// the same conversation is still a repeat (#8): saved once, confirmed twice.
// Cursor joins both hooks' messages, so the user sees "✓ Jotted #1" and
// "✓ Already jotted #1" together. That's intended: it beats saving the jot
// twice, and Cursor is unsupported anyway.
func TestCursorNoGenerationIDRepeat(t *testing.T) {
	open, path := tempDB(t)
	payload := fixture(t, "cursor", "capture_single_root", map[string]any{"generation_id": ""})
	_, a := runCursor(t, payload, open)
	_, b := runCursor(t, payload, open)
	if a.Continue || a.UserMessage != "✓ Jotted #1" || b.Continue || b.UserMessage != "✓ Already jotted #1" {
		t.Fatalf("replies: %+v, %+v", a, b)
	}
	if es := entries(t, path); len(es) != 1 {
		t.Fatalf("saved %d entries, want 1", len(es))
	}
}

// A queued or re-sent jot reaches the hook again as a new prompt (#8). It is
// still blocked, and confirmed with the first jot's number instead of saved.
func TestClaudeRepeatedJotSavedOnce(t *testing.T) {
	open, path := tempDB(t)
	cwd := t.TempDir()
	_, first := runClaude(t, fixture(t, "claude", "capture", map[string]any{"cwd": cwd}), open)
	assertBlockedOK(t, first, 1)
	for _, prompt := range []string{
		">> token cache TTL looks too long, check with infra before shipping",
		">>  token cache TTL looks  too long, check with infra before shipping ",
	} {
		_, b := runClaude(t, fixture(t, "claude", "capture", map[string]any{"cwd": cwd, "prompt": prompt}), open)
		if b == nil || b.Decision != "block" || !b.HookSpecificOutput.SuppressOriginalPrompt {
			t.Fatalf("%q: repeat must still block, got %+v", prompt, b)
		}
		if b.Reason != "✓ Already jotted #1" {
			t.Fatalf("%q: reason = %q", prompt, b.Reason)
		}
	}
	// Other text in the same session is a new jot.
	_, other := runClaude(t, fixture(t, "claude", "capture", map[string]any{"cwd": cwd, "prompt": ">> something else"}), open)
	assertBlockedOK(t, other, 2)
	if es := entries(t, path); len(es) != 2 {
		t.Fatalf("saved %d entries, want 2", len(es))
	}
}

// Another session, or no session at all, has nothing to repeat.
func TestClaudeRepeatNeedsSameSession(t *testing.T) {
	open, path := tempDB(t)
	cwd := t.TempDir()
	for i, session := range []string{"s1", "s2", "", ""} {
		_, b := runClaude(t, fixture(t, "claude", "capture", map[string]any{"cwd": cwd, "session_id": session}), open)
		assertBlockedOK(t, b, int64(i+1))
	}
	if es := entries(t, path); len(es) != 4 {
		t.Fatalf("saved %d entries, want 4", len(es))
	}
}
