// Package app is the single entry point the CLI, hooks and MCP server use.
// It owns the store and git lookups; callers never touch them directly.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/kacxx/jotaside/internal/gitctx"
	"github.com/kacxx/jotaside/internal/store"
)

// Entry is a captured thought.
type Entry = store.Entry

// ErrNotFound is returned for an unknown entry id.
var ErrNotFound = store.ErrNotFound

// Opener lazily opens a Service. Hooks call it only once a prompt is known to
// be a jot, so ordinary prompts never touch SQLite or git.
type Opener func() (*Service, error)

// Service implements jot's use cases.
type Service struct {
	store *store.Store
	git   func(ctx context.Context, dir string) gitctx.Info
	now   func() time.Time
	// desktop finds Claude Desktop's session files, for open links.
	desktop desktopLinks
}

// New wraps an open store.
func New(st *store.Store) *Service {
	return &Service{store: st, git: gitctx.Detect, now: time.Now, desktop: desktopLinks{dir: defaultDesktopDir()}}
}

// Open opens the database at path.
func Open(path string, busyTimeout time.Duration) (*Service, error) {
	st, err := store.Open(path, busyTimeout)
	if err != nil {
		return nil, err
	}
	return New(st), nil
}

// OpenDefault opens the database chosen by the environment (see DBPath).
func OpenDefault() (*Service, error) {
	path, err := DBPath()
	if err != nil {
		return nil, err
	}
	return Open(path, BusyTimeout())
}

// Close releases the database.
func (s *Service) Close() error { return s.store.Close() }

// CaptureRequest describes one jot.
type CaptureRequest struct {
	Text      string
	Source    string // "claude", "cursor", "cli"
	SessionID string
	Cwd       string
	Metadata  map[string]any
	// OnceKey, if set, names a string Metadata key that identifies the prompt
	// (e.g. Cursor's generation_id). A second capture from the same Source
	// with the same value returns the first entry instead of saving again.
	OnceKey string
}

// Capture stores a jot with best-effort git context for req.Cwd.
func (s *Service) Capture(ctx context.Context, req CaptureRequest) (Entry, error) {
	e, _, err := s.CaptureOnce(ctx, req)
	return e, err
}

// CaptureOnce is Capture that also reports whether a new entry was saved. It
// is false only when req.OnceKey matched an existing entry, which is returned.
func (s *Service) CaptureOnce(ctx context.Context, req CaptureRequest) (Entry, bool, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return Entry{}, false, errors.New("empty jot")
	}
	g := s.git(ctx, req.Cwd)
	e := Entry{
		Text:      text,
		CreatedAt: s.now(),
		Source:    req.Source,
		SessionID: req.SessionID,
		Cwd:       req.Cwd,
		RepoRoot:  g.Root,
		RepoName:  g.Name,
		Branch:    g.Branch,
		CommitSHA: g.Commit,
	}
	if len(req.Metadata) > 0 {
		b, err := json.Marshal(req.Metadata)
		if err != nil {
			return Entry{}, false, err
		}
		e.Metadata = b
	}
	if v, _ := req.Metadata[req.OnceKey].(string); req.OnceKey != "" && v != "" {
		saved, err := s.store.InsertOnce(ctx, &e, req.OnceKey, v)
		if err != nil {
			return Entry{}, false, err
		}
		return e, saved, nil
	}
	if _, err := s.store.Insert(ctx, &e); err != nil {
		return Entry{}, false, err
	}
	return e, true, nil
}

// Inbox returns the newest n inbox entries (all if n <= 0).
func (s *Service) Inbox(ctx context.Context, n int) ([]Entry, error) {
	return s.store.List(ctx, store.StatusInbox, n)
}

// InboxOlder returns the newest n inbox entries that are at least days
// calendar days old at ref (all if n <= 0). The age filter runs before the
// limit.
func (s *Service) InboxOlder(ctx context.Context, n, days int, ref time.Time) ([]Entry, error) {
	es, err := s.store.List(ctx, store.StatusInbox, 0)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, e := range es {
		if AgeDays(e.CreatedAt, ref) >= days {
			out = append(out, e)
		}
	}
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// AgeDays is how many calendar days before ref t falls, in local time. A jot
// from 23:50 yesterday is 1 day old, not 0.
func AgeDays(t, ref time.Time) int {
	y1, m1, d1 := t.Local().Date()
	y2, m2, d2 := ref.Local().Date()
	days := int(time.Date(y2, m2, d2, 0, 0, 0, 0, time.UTC).Sub(time.Date(y1, m1, d1, 0, 0, 0, 0, time.UTC)).Hours() / 24)
	if days < 0 {
		return 0
	}
	return days
}

// Show returns one entry.
func (s *Service) Show(ctx context.Context, id int64) (Entry, error) {
	return s.store.Get(ctx, id)
}

// Search returns the newest n entries containing q, any status.
func (s *Service) Search(ctx context.Context, q string, n int) ([]Entry, error) {
	if strings.TrimSpace(q) == "" {
		return nil, errors.New("empty search query")
	}
	return s.store.Search(ctx, q, n)
}

// Done marks an entry as done.
func (s *Service) Done(ctx context.Context, id int64) error {
	_, err := s.DoneAll(ctx, []int64{id}, "")
	return err
}

// DoneAll marks every entry in ids done, or none if any id is missing (the
// error is a *store.MissingError). A non-empty note records why, and replaces
// the note of a jot that was already done. The result says which ids changed:
// a jot already done and given no note is left alone and reported false, a
// jot given a note is reported true.
func (s *Service) DoneAll(ctx context.Context, ids []int64, note string) (map[int64]bool, error) {
	return s.store.Done(ctx, ids, note, time.Now())
}

// Backup writes a consistent copy of the database to dst (never overwrites).
func (s *Service) Backup(ctx context.Context, dst string) error {
	return s.store.Backup(ctx, dst)
}
