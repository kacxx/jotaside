// Package store persists jots in SQLite.
//
// Every connection is opened with busy_timeout, WAL and synchronous=NORMAL set
// in the DSN, so concurrent hook processes queue on the write lock instead of
// failing immediately, and each holds it only briefly. Each capture is a
// single autocommit INSERT.
//
// busy_timeout is not the whole story for a brand-new file: switching it to
// WAL can return SQLITE_BUSY without consulting the busy handler when several
// processes create the database at once. Open therefore retries, bounding every
// attempt by what is left of the busy timeout. Open any database through Open,
// or that race comes back.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite" // also registers the "sqlite" driver
)

// sqliteBusy is SQLITE_BUSY, the primary result code for a locked database.
const sqliteBusy = 5

// openRetryInterval is the pause between Open attempts that failed with
// SQLITE_BUSY. Short, because those failures are quick and the busy-timeout
// budget is usually a couple of seconds; attempts are bounded by what is left
// of that budget either way.
const openRetryInterval = 10 * time.Millisecond

// Statuses an entry can have.
const (
	StatusInbox = "inbox"
	StatusDone  = "done"
)

// ErrNotFound is returned when an entry id does not exist.
var ErrNotFound = errors.New("entry not found")

const timeLayout = "2006-01-02T15:04:05.000Z07:00"

const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS entries (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	text       TEXT NOT NULL,
	created_at TEXT NOT NULL,
	status     TEXT NOT NULL DEFAULT 'inbox',
	source     TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	cwd        TEXT NOT NULL DEFAULT '',
	repo_root  TEXT NOT NULL DEFAULT '',
	repo_name  TEXT NOT NULL DEFAULT '',
	branch     TEXT NOT NULL DEFAULT '',
	commit_sha TEXT NOT NULL DEFAULT '',
	metadata   TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata))
);
CREATE INDEX IF NOT EXISTS entries_status_id ON entries(status, id);
PRAGMA user_version = 1;
`

// Entry is one captured thought.
type Entry struct {
	ID        int64           `json:"id"`
	Text      string          `json:"text"`
	CreatedAt time.Time       `json:"created_at"`
	Status    string          `json:"status"`
	Source    string          `json:"source,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	Cwd       string          `json:"cwd,omitempty"`
	RepoRoot  string          `json:"repo_root,omitempty"`
	RepoName  string          `json:"repo_name,omitempty"`
	Branch    string          `json:"branch,omitempty"`
	CommitSHA string          `json:"commit_sha,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	// IssueURL is metadata's issue_url, set when the jot was promoted to an
	// issue. It is read from metadata, never written by Insert.
	IssueURL string `json:"issue_url,omitempty"`
	// DoneNote is metadata's done_note, the reason the jot was closed. Like
	// IssueURL it is read from metadata, never written by Insert.
	DoneNote string `json:"done_note,omitempty"`
	// OpenURL links to the jot's source chat. It is never stored; the app
	// layer fills it in for the entries it returns on request.
	OpenURL string `json:"open_url,omitempty"`
	// SessionTitle is the Claude Code session's name, read from its transcript
	// when the inbox is listed. It is never stored.
	SessionTitle string `json:"session_title,omitempty"`
	// OpenCommand is the shell command that opens OpenURL (jotaside open <id>),
	// with the full path to jotaside. Like OpenURL it is never stored; the MCP
	// server fills it in.
	OpenCommand string `json:"open_command,omitempty"`
}

// MetaIssueURL is the metadata key holding a promoted jot's issue URL.
const MetaIssueURL = "issue_url"

// Metadata keys written by jotaside done: why the jot was closed, and when.
const (
	MetaDoneNote = "done_note"
	MetaDoneAt   = "done_at"
)

// Metadata keys a Claude Code capture may record. MetaDesktopSession is
// Claude Desktop's own session id (local_<uuid>); it is a hint, so check it
// against Desktop's session file before relying on it.
const (
	MetaTranscriptPath   = "transcript_path"
	MetaDesktopSession   = "claude_desktop_session_id"
	MetaClaudeEntrypoint = "claude_entrypoint"
)

// Store is a handle on the jot database.
type Store struct {
	db *sql.DB
}

// DSN builds the driver data source name for path.
func DSN(path string, busyTimeout time.Duration) string {
	p := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout("+strconv.FormatInt(busyTimeout.Milliseconds(), 10)+")")
	q.Add("_pragma", "journal_mode(WAL)")
	// In WAL mode NORMAL syncs at checkpoints rather than on every commit, so
	// a writer no longer holds the write lock across a disk flush. With FULL,
	// slow flushes (Windows) let back-to-back writers starve a waiting
	// connection past its busy timeout. A commit survives an application
	// crash either way; only a power loss or OS crash can undo the newest ones.
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	return u.String() + "?" + q.Encode()
}

// Open opens (creating if needed) the database at path. It waits at most
// busyTimeout for a locked database, including while creating it.
func Open(path string, busyTimeout time.Duration) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	if err := secureDBFiles(path); err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// Each attempt gets its own handle whose busy_timeout is what is left of
	// the budget, so neither the connect-time WAL switch nor BEGIN IMMEDIATE
	// can wait past the deadline.
	deadline := time.Now().Add(busyTimeout)
	for {
		err := openAttempt(path, max(time.Until(deadline), 0))
		if err == nil {
			break
		}
		remaining := time.Until(deadline)
		if !isBusy(err) || remaining <= 0 {
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		time.Sleep(min(openRetryInterval, remaining))
	}
	// The file is now WAL with the current schema, so the pragmas in the DSN
	// no longer need a lock.
	db, err := sql.Open("sqlite", DSN(path, busyTimeout))
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// openAttempt makes one attempt at preparing the database; a variable so tests
// can simulate the WAL-switch race.
var openAttempt = migrateWithTimeout

// migrateWithTimeout connects with the given busy timeout and ensures the
// schema exists.
func migrateWithTimeout(path string, busyTimeout time.Duration) error {
	db, err := sql.Open("sqlite", DSN(path, busyTimeout))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return migrate(db)
}

// migrate creates the schema. It only takes the write lock when the schema is
// missing, so opening an existing database is read-only.
func migrate(db *sql.DB) error {
	if v, err := userVersion(db); err != nil || v >= schemaVersion {
		return err
	}
	// BEGIN IMMEDIATE (see _txlock in DSN) takes the write lock up front, where
	// busy_timeout applies, and serialises concurrent first opens. Re-check the
	// version under the lock in case another process migrated meanwhile.
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // after Commit this is a no-op returning ErrTxDone
	if v, err := userVersion(tx); err != nil || v >= schemaVersion {
		return err
	}
	if _, err := tx.Exec(schema); err != nil {
		return err
	}
	return tx.Commit()
}

// userVersion reads PRAGMA user_version from a *sql.DB or *sql.Tx.
func userVersion(q interface {
	QueryRow(query string, args ...any) *sql.Row
}) (int, error) {
	var v int
	err := q.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

func isBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqliteBusy
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for tests and diagnostics.
func (s *Store) DB() *sql.DB { return s.db }

// Insert stores e with one autocommit INSERT and returns its id.
// Status defaults to inbox and CreatedAt to now.
func (s *Store) Insert(ctx context.Context, e *Entry) (int64, error) {
	meta := e.prepare()
	res, err := s.db.ExecContext(ctx, `INSERT INTO entries
		(text, created_at, status, source, session_id, cwd, repo_root, repo_name, branch, commit_sha, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Text, e.CreatedAt.UTC().Format(timeLayout), e.Status, e.Source, e.SessionID, e.Cwd,
		e.RepoRoot, e.RepoName, e.Branch, e.CommitSHA, meta)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	e.ID = id
	return id, nil
}

// Dedup says which earlier entries make an insert a duplicate. The zero value
// checks nothing.
type Dedup struct {
	// Key and Value, if Value is set, name the prompt: an entry with the same
	// source and metadata[Key] == Value is the same prompt saved by another
	// hook. Key must be letters, digits and underscores, as it is spliced into
	// a JSON path.
	Key, Value string
	// Since, if set, makes an inbox entry from the same session created at or
	// after Since with the same text, ignoring whitespace differences, a
	// repeat. A done entry doesn't count: jotting it again brings it back.
	// Entries without a session are never repeats.
	Since time.Time
}

// InsertResult says what InsertDedup did.
type InsertResult int

const (
	// Inserted means the entry was saved.
	Inserted InsertResult = iota
	// SamePrompt means another hook already saved this prompt (Dedup.Key).
	SamePrompt
	// Repeat means the session saved the same text recently (Dedup.Since).
	Repeat
)

// InsertDedup stores e unless d finds a duplicate, in which case *e is
// replaced by the earlier entry. A same-prompt match wins over a repeat. The
// checks and the insert run in one BEGIN IMMEDIATE transaction (see _txlock in
// DSN), so two processes saving the same jot at once store it once.
//
// The checks scan entries: metadata is not indexed and text is compared in
// Go. Only jots reach them, and a personal jot database stays small.
func (s *Store) InsertDedup(ctx context.Context, e *Entry, d Dedup) (InsertResult, error) {
	if d.Value != "" && !jsonKey.MatchString(d.Key) {
		return Inserted, fmt.Errorf("invalid metadata key %q", d.Key)
	}
	meta := e.prepare()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Inserted, err
	}
	defer func() { _ = tx.Rollback() }() // after Commit this is a no-op returning ErrTxDone
	if d.Value != "" {
		rows, err := tx.QueryContext(ctx, `SELECT `+columns+` FROM entries
			WHERE source = ? AND json_extract(metadata, ?) = ? ORDER BY id LIMIT 1`,
			e.Source, `$."`+d.Key+`"`, d.Value)
		if err != nil {
			return Inserted, err
		}
		es, err := scan(rows)
		if err != nil {
			return Inserted, err
		}
		if len(es) > 0 {
			*e = es[0]
			return SamePrompt, nil
		}
	}
	if !d.Since.IsZero() && e.SessionID != "" {
		rows, err := tx.QueryContext(ctx, `SELECT `+columns+` FROM entries
			WHERE session_id = ? AND status = ? AND created_at >= ? ORDER BY id`,
			e.SessionID, StatusInbox, d.Since.UTC().Format(timeLayout))
		if err != nil {
			return Inserted, err
		}
		es, err := scan(rows)
		if err != nil {
			return Inserted, err
		}
		for _, prev := range es {
			if sameJotText(prev.Text, e.Text) {
				*e = prev
				return Repeat, nil
			}
		}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO entries
		(text, created_at, status, source, session_id, cwd, repo_root, repo_name, branch, commit_sha, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Text, e.CreatedAt.UTC().Format(timeLayout), e.Status, e.Source, e.SessionID, e.Cwd,
		e.RepoRoot, e.RepoName, e.Branch, e.CommitSHA, meta)
	if err != nil {
		return Inserted, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Inserted, err
	}
	if err := tx.Commit(); err != nil {
		return Inserted, err
	}
	e.ID = id
	return Inserted, nil
}

// sameJotText reports whether two jots say the same thing: equal once runs of
// whitespace are collapsed and the ends trimmed. Case counts.
func sameJotText(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

var jsonKey = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// prepare fills e's defaults (status inbox, CreatedAt now) and returns its
// metadata as stored.
func (e *Entry) prepare() string {
	if e.Status == "" {
		e.Status = StatusInbox
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	meta := string(e.Metadata)
	if meta == "" || meta == "null" {
		return "{}"
	}
	return meta
}

const columns = `id, text, created_at, status, source, session_id, cwd, repo_root, repo_name, branch, commit_sha, metadata`

// Get returns the entry with the given id.
func (s *Store) Get(ctx context.Context, id int64) (Entry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM entries WHERE id = ?`, id)
	if err != nil {
		return Entry{}, err
	}
	es, err := scan(rows)
	if err != nil {
		return Entry{}, err
	}
	if len(es) == 0 {
		return Entry{}, ErrNotFound
	}
	return es[0], nil
}

// List returns the newest entries with the given status (all if empty).
func (s *Store) List(ctx context.Context, status string, limit int) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM entries
		WHERE (? = '' OR status = ?) ORDER BY id DESC LIMIT ?`, status, status, limitOrAll(limit))
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

// BySessions returns every entry whose session id is one of ids, newest first.
func (s *Store) BySessions(ctx context.Context, ids []string) ([]Entry, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM entries
		WHERE session_id IN (`+marks+`) ORDER BY id DESC`, args...)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

// Search returns the newest entries whose text or done note contains q
// (case-insensitive for ASCII). LIKE wildcards in q are matched literally.
func (s *Store) Search(ctx context.Context, q string, limit int) ([]Entry, error) {
	pat := "%" + EscapeLike(q) + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM entries
		WHERE text LIKE ? ESCAPE '\'
		   OR json_extract(metadata, '$.done_note') LIKE ? ESCAPE '\'
		ORDER BY id DESC LIMIT ?`, pat, pat, limitOrAll(limit))
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

// SetStatus changes an entry's status.
func (s *Store) SetStatus(ctx context.Context, id int64, status string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE entries SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetMetadata sets metadata[key] = value and, unless status is empty, the
// status, in one transaction. Other metadata keys are kept.
func (s *Store) SetMetadata(ctx context.Context, id int64, key string, value any, status string) error {
	v, err := json.Marshal(value)
	if err != nil {
		return err
	}
	// BEGIN IMMEDIATE (see _txlock in DSN): the read below and the update can't
	// interleave with another writer.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // after Commit this is a no-op returning ErrTxDone
	var raw, cur string
	err = tx.QueryRowContext(ctx, `SELECT metadata, status FROM entries WHERE id = ?`, id).Scan(&raw, &cur)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	meta := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil || meta == nil {
		return fmt.Errorf("entry %d: metadata is not a JSON object", id)
	}
	meta[key] = v
	if status == StatusDone && cur != StatusDone {
		meta[MetaDoneAt] = json.RawMessage(strconv.Quote(time.Now().UTC().Format(timeLayout)))
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if status == "" {
		status = cur
	}
	if _, err := tx.ExecContext(ctx, `UPDATE entries SET metadata = ?, status = ? WHERE id = ?`,
		string(b), status, id); err != nil {
		return err
	}
	return tx.Commit()
}

// MissingError is returned by Done when some ids do not exist. It matches
// ErrNotFound.
type MissingError struct{ IDs []int64 }

func (e *MissingError) Error() string {
	parts := make([]string, len(e.IDs))
	for i, id := range e.IDs {
		parts[i] = fmt.Sprintf("#%d", id)
	}
	if len(parts) == 1 {
		return "no jot " + parts[0]
	}
	return "no jots " + strings.Join(parts, ", ")
}

func (e *MissingError) Is(target error) bool { return target == ErrNotFound }

// Done marks every entry in ids done in one transaction. If any id does not
// exist it returns a *MissingError naming all of them and changes nothing.
// A non-empty note is stored as metadata done_note, replacing an earlier one;
// done_at is set when the entry was not already done. Closing a done entry
// without a note changes nothing. The result holds the ids that were changed.
func (s *Store) Done(ctx context.Context, ids []int64, note string, now time.Time) (map[int64]bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	type row struct {
		meta map[string]json.RawMessage
		done bool
	}
	rows := make(map[int64]row, len(ids))
	var missing []int64
	for _, id := range ids {
		if _, seen := rows[id]; seen {
			continue
		}
		var raw, status string
		err := tx.QueryRowContext(ctx, `SELECT metadata, status FROM entries WHERE id = ?`, id).Scan(&raw, &status)
		if errors.Is(err, sql.ErrNoRows) {
			missing = append(missing, id)
			rows[id] = row{}
			continue
		}
		if err != nil {
			return nil, err
		}
		meta := map[string]json.RawMessage{}
		if err := json.Unmarshal([]byte(raw), &meta); err != nil || meta == nil {
			return nil, fmt.Errorf("entry %d: metadata is not a JSON object", id)
		}
		rows[id] = row{meta: meta, done: status == StatusDone}
	}
	if len(missing) > 0 {
		return nil, &MissingError{IDs: missing}
	}
	changed := make(map[int64]bool, len(rows))
	for id, r := range rows {
		if r.done && note == "" {
			continue
		}
		changed[id] = true
		if note != "" {
			v, _ := json.Marshal(note)
			r.meta[MetaDoneNote] = v
		}
		if !r.done {
			v, _ := json.Marshal(now.UTC().Format(timeLayout))
			r.meta[MetaDoneAt] = v
		}
		b, err := json.Marshal(r.meta)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE entries SET metadata = ?, status = ? WHERE id = ?`,
			string(b), StatusDone, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return changed, nil
}

// Backup writes a consistent copy of the database to dst using VACUUM INTO.
// It refuses to overwrite an existing file. The backup is owner-only: dst is
// created exclusively with filePerm first, and VACUUM INTO accepts an empty
// file, so there is no moment when the copy is readable by others.
func (s *Store) Backup(ctx context.Context, dst string) (err error) {
	if strings.TrimSpace(dst) == "" {
		return errors.New("backup: destination path is empty")
	}
	// VACUUM INTO reads a destination starting with "file:" as a URI, which
	// would write the copy somewhere other than the file created below. An
	// absolute path never starts with "file:".
	if dst, err = filepath.Abs(dst); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("backup: %s already exists; refusing to overwrite", dst)
	}
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	// From here on dst is ours: remove it on any failure.
	defer func() {
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	if err := f.Close(); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	return nil
}

// EscapeLike escapes the LIKE metacharacters %, _ and the escape character \.
func EscapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func limitOrAll(n int) int {
	if n <= 0 {
		return -1
	}
	return n
}

func scan(rows *sql.Rows) ([]Entry, error) {
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var created, meta string
		if err := rows.Scan(&e.ID, &e.Text, &created, &e.Status, &e.Source, &e.SessionID, &e.Cwd,
			&e.RepoRoot, &e.RepoName, &e.Branch, &e.CommitSHA, &meta); err != nil {
			return nil, err
		}
		t, err := time.Parse(timeLayout, created)
		if err != nil {
			return nil, fmt.Errorf("entry %d: bad created_at %q: %w", e.ID, created, err)
		}
		e.CreatedAt = t
		if meta != "" && meta != "{}" {
			e.Metadata = json.RawMessage(meta)
			var m map[string]any
			if json.Unmarshal(e.Metadata, &m) == nil {
				e.IssueURL, _ = m[MetaIssueURL].(string)
				e.DoneNote, _ = m[MetaDoneNote].(string)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
