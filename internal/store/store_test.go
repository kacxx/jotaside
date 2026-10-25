package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub dir", "jot.db")
	s, err := Open(path, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func TestCRUD(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)

	e := Entry{
		Text: "first", Source: "claude", SessionID: "sess", Cwd: "/w",
		RepoRoot: "/w", RepoName: "w", Branch: "main", CommitSHA: "abc1234",
		Metadata: json.RawMessage(`{"k":"v"}`),
	}
	id, err := s.Insert(ctx, &e)
	if err != nil || id != 1 {
		t.Fatalf("insert: id=%d err=%v", id, err)
	}
	if _, err := s.Insert(ctx, &Entry{Text: "second"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "first" || got.Status != StatusInbox || got.Branch != "main" ||
		got.CommitSHA != "abc1234" || string(got.Metadata) != `{"k":"v"}` ||
		got.CreatedAt.IsZero() {
		t.Fatalf("get: %+v", got)
	}
	if _, err := s.Get(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}

	list, err := s.List(ctx, StatusInbox, 10)
	if err != nil || len(list) != 2 || list[0].ID != 2 {
		t.Fatalf("list newest first: %v %+v", err, list)
	}
	if list, _ := s.List(ctx, StatusInbox, 1); len(list) != 1 {
		t.Fatalf("limit: %d", len(list))
	}

	if err := s.SetStatus(ctx, 1, StatusDone); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus(ctx, 42, StatusDone); !errors.Is(err, ErrNotFound) {
		t.Fatalf("done missing: %v", err)
	}
	list, _ = s.List(ctx, StatusInbox, 0)
	if len(list) != 1 || list[0].ID != 2 {
		t.Fatalf("inbox after done: %+v", list)
	}
	all, _ := s.List(ctx, "", 0)
	if len(all) != 2 {
		t.Fatalf("all: %d", len(all))
	}
}

func TestBadMetadataRejected(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.Insert(context.Background(), &Entry{Text: "x", Metadata: json.RawMessage(`{nope`)}); err == nil {
		t.Fatal("expected invalid JSON metadata to be rejected")
	}
}

func TestSearchEscapesWildcards(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	for _, txt := range []string{"100% done", "100x done", "a_b", "axb", `back\slash`, "Case Test"} {
		if _, err := s.Insert(ctx, &Entry{Text: txt}); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string][]string{
		"100%": {"100% done"},
		"a_b":  {"a_b"},
		`k\s`:  {`back\slash`},
		"case": {"Case Test"},
		"done": {"100x done", "100% done"},
		"%":    {"100% done"},
		"_":    {"a_b"},
		"zzz":  nil,
	}
	for q, want := range cases {
		got, err := s.Search(ctx, q, 0)
		if err != nil {
			t.Fatal(err)
		}
		var texts []string
		for _, e := range got {
			texts = append(texts, e.Text)
		}
		if fmt.Sprint(texts) != fmt.Sprint(want) {
			t.Errorf("Search(%q) = %q, want %q", q, texts, want)
		}
	}
}

func TestWALEnabled(t *testing.T) {
	s, path := openTemp(t)
	var mode string
	if err := s.DB().QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var busy int
	if err := s.DB().QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatal(err)
	}
	if busy != 2000 {
		t.Fatalf("busy_timeout = %d, want 2000", busy)
	}
	var sync int
	if err := s.DB().QueryRow("PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatal(err)
	}
	if sync != 1 { // NORMAL
		t.Fatalf("synchronous = %d, want 1 (NORMAL)", sync)
	}
	if _, err := s.Insert(context.Background(), &Entry{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("expected WAL file: %v", err)
	}
}

// TestConcurrentWriterPools simulates several hook processes capturing at once:
// each goroutine has its own pool, as a separate process would.
func TestConcurrentWriterPools(t *testing.T) {
	const pools, perPool = 8, 50
	path := filepath.Join(t.TempDir(), "jot.db")
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, pools*perPool)
	for p := 0; p < pools; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			s, err := Open(path, 5*time.Second)
			if err != nil {
				errs <- err
				return
			}
			defer s.Close()
			for i := 0; i < perPool; i++ {
				if _, err := s.Insert(ctx, &Entry{Text: fmt.Sprintf("p%d-%d", p, i)}); err != nil {
					errs <- err
				}
			}
		}(p)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	s, err := Open(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n, distinct int
	if err := s.DB().QueryRow("SELECT count(*), count(DISTINCT text) FROM entries").Scan(&n, &distinct); err != nil {
		t.Fatal(err)
	}
	if n != pools*perPool || distinct != n {
		t.Fatalf("rows = %d distinct = %d, want %d", n, distinct, pools*perPool)
	}
}

// TestConcurrentFirstOpen races several openers on a file that does not exist
// yet: creating it and switching it to WAL must not fail with SQLITE_BUSY.
func TestConcurrentFirstOpen(t *testing.T) {
	const trials, openers = 40, 8
	for trial := 0; trial < trials; trial++ {
		path := filepath.Join(t.TempDir(), "jot.db")
		var wg sync.WaitGroup
		errs := make(chan error, openers)
		for g := 0; g < openers; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s, err := Open(path, 5*time.Second)
				if err != nil {
					errs <- err
					return
				}
				defer s.Close()
				if _, err := s.Insert(context.Background(), &Entry{Text: "x"}); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("trial %d: %v", trial, err)
		}
	}
}

func TestBackup(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	for i := 0; i < 3; i++ {
		if _, err := s.Insert(ctx, &Entry{Text: fmt.Sprint("e", i)}); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(ctx, dst); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dst, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	list, err := b.List(ctx, "", 0)
	if err != nil || len(list) != 3 {
		t.Fatalf("backup contents: %v %d", err, len(list))
	}

	before, _ := os.ReadFile(dst)
	err = s.Backup(ctx, dst)
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("second backup: %v", err)
	}
	after, _ := os.ReadFile(dst)
	if string(before) != string(after) {
		t.Fatal("backup file was modified")
	}
}

func TestSetMetadata(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.Insert(ctx, &Entry{Text: "a", Metadata: json.RawMessage(`{"k":"v"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Insert(ctx, &Entry{Text: "b"}); err != nil {
		t.Fatal(err)
	}

	if err := s.SetMetadata(ctx, 1, MetaIssueURL, "https://x/1", StatusDone); err != nil {
		t.Fatal(err)
	}
	e, err := s.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(e.Metadata, &m); err != nil || m["k"] != "v" || m[MetaIssueURL] != "https://x/1" {
		t.Fatalf("metadata: %s (%v)", e.Metadata, err)
	}
	if e.Status != StatusDone || e.IssueURL != "https://x/1" {
		t.Fatalf("entry: %+v", e)
	}

	// An empty status keeps the current one; empty metadata becomes an object.
	if err := s.SetMetadata(ctx, 2, "n", 3, ""); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Get(ctx, 2); e.Status != StatusInbox || string(e.Metadata) != `{"n":3}` || e.IssueURL != "" {
		t.Fatalf("entry 2: %+v", e)
	}

	if err := s.SetMetadata(ctx, 99, "k", "v", StatusDone); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id: %v", err)
	}
}

func TestInsertDedupPrompt(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	once := func(v string) Dedup { return Dedup{Key: "generation_id", Value: v} }

	first := Entry{Text: "a", Source: "cursor", Metadata: json.RawMessage(`{"generation_id":"g1"}`)}
	if r, err := s.InsertDedup(ctx, &first, once("g1")); r != Inserted || err != nil || first.ID != 1 {
		t.Fatalf("first: r=%v id=%d err=%v", r, first.ID, err)
	}
	dup := Entry{Text: "a again", Source: "cursor", Metadata: json.RawMessage(`{"generation_id":"g1"}`)}
	if r, err := s.InsertDedup(ctx, &dup, once("g1")); r != SamePrompt || err != nil || dup.ID != 1 || dup.Text != "a" {
		t.Fatalf("dup: r=%v entry=%+v err=%v", r, dup, err)
	}
	// Another source, or another value, is a different prompt.
	other := Entry{Text: "b", Source: "claude", Metadata: json.RawMessage(`{"generation_id":"g1"}`)}
	if r, err := s.InsertDedup(ctx, &other, once("g1")); r != Inserted || err != nil || other.ID != 2 {
		t.Fatalf("other source: r=%v id=%d err=%v", r, other.ID, err)
	}
	next := Entry{Text: "c", Source: "cursor", Metadata: json.RawMessage(`{"generation_id":"g2"}`)}
	if r, err := s.InsertDedup(ctx, &next, once("g2")); r != Inserted || err != nil || next.ID != 3 {
		t.Fatalf("other value: r=%v id=%d err=%v", r, next.ID, err)
	}
	for _, key := range []string{`a"b`, "a.b", "$"} {
		if _, err := s.InsertDedup(ctx, &Entry{Text: "d", Source: "cursor"}, Dedup{Key: key, Value: "v"}); err == nil {
			t.Errorf("key %q: want error", key)
		}
	}
}

func TestInsertDedupRepeat(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	t0 := time.Date(2026, 10, 22, 7, 44, 48, 90_000_000, time.UTC)
	window := time.Minute
	insert := func(text, session string, at time.Time) (Entry, InsertResult) {
		t.Helper()
		e := Entry{Text: text, Source: "claude", SessionID: session, CreatedAt: at}
		r, err := s.InsertDedup(ctx, &e, Dedup{Since: at.Add(-window)})
		if err != nil {
			t.Fatal(err)
		}
		return e, r
	}

	first, r := insert("need to get the ArgoCD MCP added", "s1", t0)
	if r != Inserted || first.ID != 1 {
		t.Fatalf("first: r=%v id=%d", r, first.ID)
	}
	for _, tc := range []struct {
		name string
		text string
		at   time.Time
	}{
		{"queued pair, 35 ms apart", "need to get the ArgoCD MCP added", t0.Add(35 * time.Millisecond)},
		{"re-sent seconds later", "need to get the ArgoCD MCP added", t0.Add(8 * time.Second)},
		{"whitespace differs", "need  to get the\tArgoCD MCP added", t0.Add(20 * time.Second)},
		{"at the edge of the window", "need to get the ArgoCD MCP added", t0.Add(window)},
	} {
		got, r := insert(tc.text, "s1", tc.at)
		if r != Repeat || got.ID != 1 || got.Text != first.Text {
			t.Errorf("%s: r=%v entry=%+v, want Repeat of #1", tc.name, r, got)
		}
	}
	for _, tc := range []struct {
		name    string
		text    string
		session string
		at      time.Time
	}{
		{"after the window", "need to get the ArgoCD MCP added", "s1", t0.Add(window + time.Millisecond)},
		{"another session", "need to get the ArgoCD MCP added", "s2", t0.Add(time.Second)},
		{"different case", "Need to get the ArgoCD MCP added", "s1", t0.Add(2 * time.Second)},
		{"different text", "need to get the ArgoCD MCP removed", "s1", t0.Add(3 * time.Second)},
	} {
		if _, r := insert(tc.text, tc.session, tc.at); r != Inserted {
			t.Errorf("%s: r=%v, want Inserted", tc.name, r)
		}
	}
	// A done jot isn't a repeat: jotting it again brings it back.
	closed, _ := insert("check the TTL", "s3", t0)
	if _, err := s.Done(ctx, []int64{closed.ID}, "", t0); err != nil {
		t.Fatal(err)
	}
	if again, r := insert("check the TTL", "s3", t0.Add(4*time.Second)); r != Inserted || again.ID == closed.ID || again.Status != StatusInbox {
		t.Errorf("after done: r=%v entry=%+v, want a new inbox entry", r, again)
	}
	// Without a session there is nothing to compare against.
	for i := range 2 {
		e := Entry{Text: "same", Source: "cli", CreatedAt: t0}
		if r, err := s.InsertDedup(ctx, &e, Dedup{Since: t0.Add(-window)}); r != Inserted || err != nil {
			t.Fatalf("no session #%d: r=%v err=%v", i, r, err)
		}
	}
}

func TestInsertDedupBoth(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	t0 := time.Date(2026, 10, 22, 7, 44, 48, 0, time.UTC)
	add := func(gen string, at time.Time) (Entry, InsertResult) {
		t.Helper()
		e := Entry{Text: "x", Source: "cursor", SessionID: "c1", CreatedAt: at,
			Metadata: json.RawMessage(`{"generation_id":"` + gen + `"}`)}
		r, err := s.InsertDedup(ctx, &e, Dedup{Key: "generation_id", Value: gen, Since: at.Add(-time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		return e, r
	}
	if _, r := add("g1", t0); r != Inserted {
		t.Fatalf("first: %v", r)
	}
	// The same prompt wins over a repeat: the second hook stays silent.
	if e, r := add("g1", t0.Add(time.Millisecond)); r != SamePrompt || e.ID != 1 {
		t.Fatalf("same prompt: r=%v id=%d", r, e.ID)
	}
	if e, r := add("g2", t0.Add(5*time.Second)); r != Repeat || e.ID != 1 {
		t.Fatalf("new prompt, same text: r=%v id=%d", r, e.ID)
	}
}

// Hooks for two queued prompts can run at once. The repeat check and the
// insert share a transaction, so only one saves.
func TestInsertDedupRepeatConcurrently(t *testing.T) {
	ctx := context.Background()
	_, path := openTemp(t)
	t0 := time.Date(2026, 10, 22, 7, 44, 48, 0, time.UTC)
	const n = 8
	results := make(chan InsertResult, n)
	errs := make(chan error, n)
	for i := range n {
		go func() {
			// A store per goroutine, as each hook is its own process.
			s, err := Open(path, 5*time.Second)
			if err != nil {
				errs <- err
				return
			}
			defer s.Close()
			at := t0.Add(time.Duration(i) * time.Millisecond)
			e := Entry{Text: "same jot", Source: "claude", SessionID: "s1", CreatedAt: at}
			r, err := s.InsertDedup(ctx, &e, Dedup{Since: at.Add(-time.Minute)})
			if err != nil {
				errs <- err
				return
			}
			results <- r
		}()
	}
	inserted := 0
	for range n {
		select {
		case err := <-errs:
			t.Fatal(err)
		case r := <-results:
			if r == Inserted {
				inserted++
			} else if r != Repeat {
				t.Errorf("result %v", r)
			}
		}
	}
	if inserted != 1 {
		t.Errorf("%d inserts, want 1", inserted)
	}
}

func TestSameJotText(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"a b", "a b", true},
		{" a  b ", "a b", true},
		{"a\tb\nc", "a b c", true},
		{"a\u00a0b", "a b", true},
		{"a b", "A b", false},
		{"a b", "ab", false},
	} {
		if got := sameJotText(tc.a, tc.b); got != tc.want {
			t.Errorf("sameJotText(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestBySessions(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	for _, e := range []Entry{
		{Text: "a1", SessionID: "a"}, {Text: "b1", SessionID: "b"},
		{Text: "none"}, {Text: "a2", SessionID: "a"}, {Text: "c1", SessionID: "c"},
	} {
		if _, err := s.Insert(ctx, &e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.BySessions(ctx, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, e := range got {
		texts = append(texts, e.Text)
	}
	if want := "a2 b1 a1"; strings.Join(texts, " ") != want {
		t.Errorf("got %v, want newest first %q", texts, want)
	}
	if got, err := s.BySessions(ctx, nil); err != nil || len(got) != 0 {
		t.Errorf("no ids: got %v, %v", got, err)
	}
}

func TestDone(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	for _, txt := range []string{"a", "b", "c"} {
		if _, err := s.Insert(ctx, &Entry{Text: txt, Metadata: json.RawMessage(`{"k":"v"}`)}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 6, 1, 2, 3, 0, time.UTC)
	var miss *MissingError
	_, err := s.Done(ctx, []int64{1, 9, 2, 8}, "n", now)
	if !errors.As(err, &miss) || !errors.Is(err, ErrNotFound) || len(miss.IDs) != 2 {
		t.Fatalf("missing: %v", err)
	}
	if e, _ := s.Get(ctx, 1); e.Status != StatusInbox || e.DoneNote != "" {
		t.Fatalf("batch must be all or nothing: %+v", e)
	}
	if ch, err := s.Done(ctx, []int64{1, 2, 1}, "why", now); err != nil || len(ch) != 2 {
		t.Fatal(err)
	}
	e, _ := s.Get(ctx, 1)
	if e.Status != StatusDone || e.DoneNote != "why" || !strings.Contains(string(e.Metadata), `"k":"v"`) || !strings.Contains(string(e.Metadata), "done_at") {
		t.Fatalf("done: %+v %s", e, e.Metadata)
	}
	// Done again without a note changes nothing; with one, replaces it.
	if ch, err := s.Done(ctx, []int64{1}, "", now.Add(time.Hour)); err != nil || ch[1] {
		t.Fatal(err)
	}
	if e2, _ := s.Get(ctx, 1); e2.DoneNote != "why" || string(e2.Metadata) != string(e.Metadata) {
		t.Fatalf("no-note redo: %s", e2.Metadata)
	}
	if ch, err := s.Done(ctx, []int64{1}, "new", now.Add(time.Hour)); err != nil || !ch[1] {
		t.Fatal(err)
	}
	e3, _ := s.Get(ctx, 1)
	if e3.DoneNote != "new" || !strings.Contains(string(e3.Metadata), "2026-10-06T01:02:03") {
		t.Fatalf("done_at must keep the first close: %s", e3.Metadata)
	}
	// Without a note, a jot has none and is searchable by text only.
	if _, err := s.Done(ctx, []int64{3}, "", now); err != nil {
		t.Fatal(err)
	}
	if es, _ := s.Search(ctx, "new", 0); len(es) != 1 || es[0].ID != 1 {
		t.Fatalf("search note: %+v", es)
	}
}

func TestSetMetadataDoneAt(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.Insert(ctx, &Entry{Text: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMetadata(ctx, 1, MetaIssueURL, "https://x/1", StatusDone); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Get(ctx, 1)
	if !strings.Contains(string(e.Metadata), `"done_at"`) || e.Status != StatusDone {
		t.Fatalf("closing through SetMetadata records done_at: %s", e.Metadata)
	}
}
