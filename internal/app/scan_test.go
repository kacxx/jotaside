package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Synthetic transcripts in testdata/transcripts (a Claude Code projects
// directory).
const (
	cliSessionID    = "11111111-0000-4000-8000-000000000001"
	deskSessionID   = "22222222-0000-4000-8000-000000000002"
	schedSessionID  = "33333333-0000-4000-8000-000000000003"
	readerSessionID = "44444444-0000-4000-8000-000000000004"
)

func transcriptsFixture(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "transcripts"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func scanService(t *testing.T) *Service {
	t.Helper()
	svc := sessionService(t)
	svc.SetTranscriptsDir(transcriptsFixture(t))
	return svc
}

func sessionIDs(ss []Session) []string {
	var ids []string
	for _, s := range ss {
		ids = append(ids, s.ID)
	}
	slices.Sort(ids)
	return ids
}

func TestFindScansTranscripts(t *testing.T) {
	ctx := context.Background()
	svc := scanService(t)
	for _, tc := range []struct {
		q    string
		want []string
	}{
		// Two keys in one typed prompt; a lowercase branch in a tool call;
		// slash-command arguments; a tool call's nested input; a subagent's.
		{"ABC-1", []string{cliSessionID, deskSessionID}},
		{"ABC-2", []string{cliSessionID, deskSessionID}},
		{"abc-2", []string{cliSessionID, deskSessionID}},
		// Whole word: "_" ends a key; a longer key or prefix doesn't match.
		{"SUP-4821", []string{deskSessionID}},
		{"SUP-482", nil},
		// Never read: loaded context, attachments, tool output, the model's
		// text, meta records, request blobs, harness output, a summary of an
		// earlier conversation, the slash command's own text.
		{"CTX-3", nil},
		{"LOAD-8", nil},
		{"OUT-7", nil},
		{"REPLY-5", nil},
		{"META-4", nil},
		{"SYS-5", nil},
		{"LOCAL-6", nil},
		{"SUM-6", nil},
		{"CMD-9", nil},
		// Any other query is a substring, any case.
		{"Cache Bug", []string{deskSessionID}},
		// A prompt queued while the agent was busy, and a ! command, were
		// typed; a task notification and a command's output weren't.
		{"QUEUE-1", []string{cliSessionID}},
		{"BASH-3", []string{cliSessionID}},
		{"TASK-2", nil},
		{"STDOUT-4", nil},
		// A session whose transcript is gone can't be resumed.
		{"ORPHAN-1", nil},
	} {
		ss, loose, err := svc.Find(ctx, tc.q)
		if err != nil {
			t.Fatal(err)
		}
		if got := sessionIDs(ss); !slices.Equal(got, tc.want) {
			t.Errorf("find %q: got %v, want %v", tc.q, got, tc.want)
		}
		if len(loose) != 0 {
			t.Errorf("find %q: loose %v", tc.q, loose)
		}
	}
}

func TestFindScannedSessionFields(t *testing.T) {
	old := resumeGOOS
	resumeGOOS = "linux" // the cd form; Windows has its own test
	t.Cleanup(func() { resumeGOOS = old })
	svc := scanService(t)
	ss, _, err := svc.Find(context.Background(), "ABC-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ss {
		if s.Source != "claude" || len(s.Jots) != 0 || len(s.Matches) != 0 || s.LastActive.IsZero() {
			t.Errorf("scanned session: %+v", s)
		}
		want := map[string]string{cliSessionID: "/work/cli", deskSessionID: "/work/desktop"}[s.ID]
		if s.Cwd != want {
			t.Errorf("%s: cwd %q, want %q", s.ID, s.Cwd, want)
		}
		if !strings.HasPrefix(s.ResumeCommand(), "cd "+want+" && claude --resume "+s.ID) {
			t.Errorf("%s: resume %q", s.ID, s.ResumeCommand())
		}
	}
}

func TestFindMergesJotsAndTranscripts(t *testing.T) {
	ctx := context.Background()
	svc := scanService(t)
	jot(t, svc, "claude", cliSessionID, "/work/cli", "session: ABC-1") // #1
	jot(t, svc, "codex", cliSessionID, "/work/cli", "ABC-1 in codex")  // #2: another agent's session
	ss, _, err := svc.Find(ctx, "ABC-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 3 {
		t.Fatalf("got %d sessions, want 3: %+v", len(ss), ss)
	}
	n := 0
	for _, s := range ss {
		if s.Source == "claude" && s.ID == cliSessionID {
			n++
			if len(s.Jots) != 1 || len(s.Matches) != 1 || s.Matches[0].ID != 1 {
				t.Errorf("labelled session lost its jots: %+v", s)
			}
		}
	}
	if n != 1 {
		t.Fatalf("labelled and scanned session listed %d times", n)
	}
}

func TestFindOrdersByLastActive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(transcriptsFixture(t))); err != nil {
		t.Fatal(err)
	}
	old, recent := time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
	touch := func(path string, at time.Time) {
		if err := os.Chtimes(filepath.Join(dir, path), at, at); err != nil {
			t.Fatal(err)
		}
	}
	touch(filepath.Join("-work-cli", cliSessionID+".jsonl"), recent)
	touch(filepath.Join("-work-desktop", deskSessionID+".jsonl"), old)
	// A subagent that ran later makes its session more recent.
	touch(filepath.Join("-work-desktop", deskSessionID, "subagents", "agent-a1.jsonl"), time.Now())

	svc := sessionService(t)
	svc.SetTranscriptsDir(dir)
	svc.now = time.Now
	jot(t, svc, "codex", "c1", "/work", "ABC-1 from codex") // now, newest
	ss, _, err := svc.Find(ctx, "ABC-1")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range ss {
		got = append(got, s.ID)
	}
	want := []string{"c1", deskSessionID, cliSessionID}
	if !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestFindWithoutTranscripts(t *testing.T) {
	svc := sessionService(t)
	svc.SetTranscriptsDir(filepath.Join(t.TempDir(), "missing"))
	jot(t, svc, "claude", "s1", "/work", "ABC-1 note")
	ss, _, err := svc.Find(context.Background(), "ABC-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].ID != "s1" {
		t.Fatalf("got %+v", ss)
	}
}

func TestFindScanCancelled(t *testing.T) {
	svc := scanService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := svc.Find(ctx, "ABC-1"); err == nil {
		t.Fatal("want an error from a cancelled find")
	}
}

func TestScanQueriesJSONWouldEscape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "-p", "55555555-0000-4000-8000-000000000005.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"role":"user","content":"fix the \"quoted name\" bug\nand C:\\temp too"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := sessionService(t)
	svc.SetTranscriptsDir(dir)
	for _, q := range []string{`"quoted name"`, `C:\temp`, "bug\nand"} {
		ss, _, err := svc.Find(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if len(ss) != 1 {
			t.Errorf("find %q: got %d sessions, want 1", q, len(ss))
		}
	}
}

func TestContainsFoldASCII(t *testing.T) {
	for _, tc := range []struct {
		hay, needle string
		want        bool
	}{
		{"feature/ABC-2-fix", "abc-2", true},
		{"no match here", "abc", false},
		{"Ünïcode ABC", "abc", true},
		{"ÄBC", "äbc", false}, // like SQLite's LIKE: ASCII letters only
		{"anything", "", true},
	} {
		if got := containsFold(tc.hay, strings.ToLower(tc.needle)); got != tc.want {
			t.Errorf("containsFold(%q, %q) = %v", tc.hay, tc.needle, got)
		}
	}
}

func TestPrefilterChunkBoundary(t *testing.T) {
	// A key that straddles two reads of the prefilter is still found.
	dir := t.TempDir()
	path := filepath.Join(dir, "66666666-0000-4000-8000-000000000006.jsonl")
	pad := strings.Repeat("x", prefilterChunk-len(`{"type":"user","message":{"role":"user","content":"`)-3)
	line := `{"type":"user","message":{"role":"user","content":"` + pad + ` ABC-77 end"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var buf []byte
	needles := [][]byte{[]byte("abc-77"), []byte("zzz"), []byte("end\"}")}
	got := fileHits(context.Background(), path, needles, groupNeedles(needles, nil), &buf)
	if !slices.Equal(got, []bool{true, false, true}) {
		t.Fatalf("got %v", got)
	}
	if len(buf) < prefilterChunk {
		t.Fatal("the buffer isn't kept for the next file")
	}
	zzz := [][]byte{[]byte("zzz")}
	if got := fileHits(context.Background(), path, zzz, groupNeedles(zzz, nil), &buf); got != nil {
		t.Fatalf("no needle found: got %v", got)
	}
}

// FindEach reads the transcripts once for many queries and returns what Find
// returns for each.
func TestFindEach(t *testing.T) {
	ctx := context.Background()
	svc := scanService(t)
	jot(t, svc, "claude", cliSessionID, "/work/cli", "session: ABC-1")
	jot(t, svc, "cli", "", "/work", "ABC-2 from the terminal")
	qs := []string{"ABC-1", "ABC-2", "SUP-4821", "CTX-3", "QUEUE-1"}
	found, err := svc.FindEach(ctx, qs)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != len(qs) {
		t.Fatalf("got %d results for %d queries", len(found), len(qs))
	}
	for i, q := range qs {
		ss, loose, err := svc.Find(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		if a, b := sessionIDs(found[i].Sessions), sessionIDs(ss); !slices.Equal(a, b) {
			t.Errorf("%s: FindEach %v, Find %v", q, a, b)
		}
		if len(found[i].Loose) != len(loose) {
			t.Errorf("%s: FindEach %d loose, Find %d", q, len(found[i].Loose), len(loose))
		}
	}
	if _, err := svc.FindEach(ctx, []string{"ABC-1", " "}); err == nil {
		t.Error("an empty query should be an error")
	}
}

func TestMarkNeedles(t *testing.T) {
	needles := [][]byte{[]byte("abc-1"), []byte("abc-12"), []byte("abd-3"), []byte("x"), nil, []byte("zz-9")}
	groups := groupNeedles(needles, nil)
	if len(groups) != 4 { // "abc-", "abd-", "x", "zz-9"; the empty needle in none
		t.Fatalf("groups: %d", len(groups))
	}
	found := make([]bool, len(needles))
	if n := markNeedles([]byte("see abc-12 and abd-3, x"), needles, groups, found); n != 4 {
		t.Fatalf("set %d", n)
	}
	if want := []bool{true, true, true, true, false, false}; !slices.Equal(found, want) {
		t.Fatalf("found %v, want %v", found, want)
	}
	// Already found ones aren't counted again.
	if n := markNeedles([]byte("abc-1 zz-9"), needles, groups, found); n != 1 || !found[5] {
		t.Fatalf("second window: set %d, found %v", n, found)
	}
}

// writeScanFile writes JSON lines under dir.
func writeScanFile(t *testing.T, dir, rel string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func typedLine(cwd, text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "cwd": cwd, "sessionId": "x", "message": map[string]any{"role": "user", "content": text}})
	return string(b)
}

// A session id with a transcript in two project folders is one session,
// resumed from the newer; an old-style agent-<id>.jsonl counts for the
// session its records name.
func TestScanSessionLayouts(t *testing.T) {
	dir := t.TempDir()
	const id = "77777777-0000-4000-8000-000000000007"
	older := writeScanFile(t, dir, filepath.Join("-a", id+".jsonl"), typedLine("/work/a", "start ABC-5"))
	writeScanFile(t, dir, filepath.Join("-b", id+".jsonl"), typedLine("/work/b", "more on ABC-5"))
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(older, old, old); err != nil {
		t.Fatal(err)
	}
	writeScanFile(t, dir, filepath.Join("-b", "agent-1a2b3c4d.jsonl"),
		`{"type":"assistant","sessionId":"`+id+`","isSidechain":true,"message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"tracker show LEGACY-1"}}]}}`)
	svc := sessionService(t)
	svc.SetTranscriptsDir(dir)
	for _, q := range []string{"ABC-5", "LEGACY-1"} {
		ss, _, err := svc.Find(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if len(ss) != 1 || ss[0].ID != id || ss[0].Cwd != "/work/b" {
			t.Errorf("find %s: %+v", q, ss)
		}
	}
}

// owner/repo#N matches as a whole reference, in jots and transcripts.
func TestFindRepoRefWhole(t *testing.T) {
	dir := t.TempDir()
	writeScanFile(t, dir, filepath.Join("-p", "88888888-0000-4000-8000-000000000008.jsonl"), typedLine("/w", "review acme/widgets#35 and xacme/widgets#3"))
	writeScanFile(t, dir, filepath.Join("-p", "99999999-0000-4000-8000-000000000009.jsonl"), typedLine("/w", "fix acme/widgets#3."))
	svc := sessionService(t)
	svc.SetTranscriptsDir(dir)
	jot(t, svc, "codex", "c1", "/w", "acme/widgets#36 notes")
	jot(t, svc, "codex", "c2", "/w", "see acme/widgets#3, then")
	ss, _, err := svc.Find(context.Background(), " acme/widgets#3 ")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sessionIDs(ss), []string{"99999999-0000-4000-8000-000000000009", "c2"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// A compaction summary isn't typed; a prompt that quotes its opening words
// later on is.
func TestCompactSummary(t *testing.T) {
	dir := t.TempDir()
	writeScanFile(t, dir, filepath.Join("-p", "aaaaaaaa-0000-4000-8000-00000000000a.jsonl"),
		typedLine("/w", "hello"),
		`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"Summary of the chat: SUM-7 was fixed."}}`,
		typedLine("/w", "Earlier I wrote \"This session is being continued\" about QUOTE-8."))
	svc := sessionService(t)
	svc.SetTranscriptsDir(dir)
	for q, want := range map[string]int{"SUM-7": 0, "QUOTE-8": 1} {
		ss, _, err := svc.Find(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		if len(ss) != want {
			t.Errorf("find %s: %d sessions, want %d", q, len(ss), want)
		}
	}
}

// A jotted session's last activity comes from its transcript when that is
// newer, even if the scan didn't match the transcript.
func TestFindLastActiveFromTranscript(t *testing.T) {
	dir := t.TempDir()
	const id = "bbbbbbbb-0000-4000-8000-00000000000b"
	writeScanFile(t, dir, filepath.Join("-p", id+".jsonl"), typedLine("/work/start", "unrelated work"))
	later := time.Now().Add(48 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "-p", id+".jsonl"), later, later); err != nil {
		t.Fatal(err)
	}
	svc := sessionService(t)
	svc.SetTranscriptsDir(dir)
	jot(t, svc, "claude", id, "/work/start/sub", "session: ABC-9")
	ss, _, err := svc.Find(context.Background(), "ABC-9")
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || !ss[0].LastActive.Equal(later) || ss[0].Cwd != "/work/start" {
		t.Fatalf("got %+v", ss)
	}
}

func TestNeedleForEscapedQuery(t *testing.T) {
	for q, want := range map[string]string{`"`: `\"`, `\`: `\\`, "a\"bc": "bc", "\n": `\n`} {
		if got := string(newQueryMatcher(q).needle); got != want {
			t.Errorf("needle for %q: %q, want %q", q, got, want)
		}
	}
}

func toolUseLine(name, input string) string {
	return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"` + name + `","input":` + input + `}]}}`
}

// jotaside's own lookups name a key without acting on it (#35 D3, revised
// 2026-11-13); any other tool call naming it still counts.
func TestLookupsDontCount(t *testing.T) {
	dir := t.TempDir()
	const (
		bash   = "c0000000-0000-4000-8000-00000000000c"
		path   = "c1000000-0000-4000-8000-00000000000c"
		mcp    = "d0000000-0000-4000-8000-00000000000d"
		real   = "e0000000-0000-4000-8000-00000000000e"
		sub    = "f0000000-0000-4000-8000-00000000000f"
		search = "f1000000-0000-4000-8000-00000000000f"
	)
	for id, tool := range map[string]string{
		bash:   toolUseLine("Bash", `{"command":"jotaside find ABC-1"}`),
		path:   toolUseLine("Bash", `{"command":"JOT_DB=/tmp/x.db /usr/local/bin/jotaside show 12 | grep ABC-1"}`),
		mcp:    toolUseLine("mcp__jotaside__find", `{"query":"ABC-1"}`),
		search: toolUseLine("mcp__jotaside__search", `{"query":"ABC-1 cache"}`),
		real:   toolUseLine("Bash", `{"command":"git checkout -b ABC-1-fix"}`),
		sub:    toolUseLine("mcp__tracker__getIssue", `{"key":"ABC-1"}`),
	} {
		writeScanFile(t, dir, filepath.Join("-p", id+".jsonl"), typedLine("/w", "where was that?"), tool)
	}
	// A lookup in a subagent doesn't count either.
	writeScanFile(t, dir, filepath.Join("-p", bash, "subagents", "agent-1.jsonl"), toolUseLine("Bash", `{"command":"jotaside   search ABC-1"}`))
	svc := sessionService(t)
	svc.SetTranscriptsDir(dir)
	ss, _, err := svc.Find(context.Background(), "ABC-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sessionIDs(ss), []string{real, sub}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestIsLookup(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        bool
	}{
		{"Bash", `{"command":"jotaside find ABC-1"}`, true},
		{"Bash", `{"command":"cd x && jotaside search 'ABC-1'"}`, true},
		{"Bash", `{"command":"jotaside show 88"}`, true},
		{"Bash", `{"command":"jotaside done 88"}`, false},
		{"Bash", `{"command":"myjotaside find ABC-1"}`, false},
		{"Bash", `{"command":"jotasidefind ABC-1"}`, false},
		{"mcp__jotaside__find", `{"query":"ABC-1"}`, true},
		{"mcp__jotaside__activity", `{"id":3}`, true},
		{"mcp__other__find", `{"query":"ABC-1"}`, false},
		{"Grep", `{"pattern":"jotaside find ABC-1"}`, false},
	} {
		b := contentBlock{Type: "tool_use", Name: tc.name, Input: json.RawMessage(tc.input)}
		if got := isLookup(b); got != tc.want {
			t.Errorf("isLookup(%s %s) = %v", tc.name, tc.input, got)
		}
	}
}
