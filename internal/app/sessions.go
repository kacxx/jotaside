package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// SessionPrefix starts a jot that labels its session (">> session: SUP-4821 …").
const SessionPrefix = "session:"

// Session is the jots captured in one agent conversation.
type Session struct {
	Source string
	ID     string
	// Cwd is, for Claude Code, the directory the session started in, read from
	// its transcript; otherwise, or if the transcript is gone, the working
	// directory of its first jot that has one.
	Cwd   string
	Jots  []Entry // oldest first
	Label Entry   // the newest "session:" jot with a name, else the first jot
	// Matches are the session's jots that matched a Find query, oldest first.
	Matches []Entry
	// OpenURL links to the chat, set by Find; "" when there is no link.
	OpenURL string
	// LastActive is when its newest jot was saved or, for a session Find
	// found in Claude Code's transcripts, its transcript last changed if that
	// is later.
	LastActive time.Time
}

// Latest returns the session's newest jot. A session Find found only in
// Claude Code's transcripts has none, and returns the zero Entry.
func (s Session) Latest() Entry {
	if len(s.Jots) == 0 {
		return Entry{}
	}
	return s.Jots[len(s.Jots)-1]
}

// LabelText is the label's first line, or for a "session:" jot the first line
// of its name.
func (s Session) LabelText() string {
	if name, ok := sessionName(s.Label.Text); ok {
		return name
	}
	line, _, _ := strings.Cut(s.Label.Text, "\n")
	return line
}

// resumeGOOS is the OS the resume command is written for; tests replace it.
var resumeGOOS = runtime.GOOS

// ResumeCommand returns a shell command that reopens the session, or "" if
// its agent has no resume command jotaside knows about.
func (s Session) ResumeCommand() string {
	switch s.Source {
	case "claude":
		cmd := "claude --resume " + shellQuote(s.ID)
		if s.Cwd == "" {
			return cmd
		}
		// Claude Code stores sessions under the directory they started in, so
		// resume from there.
		if resumeGOOS == "windows" {
			// Windows PowerShell 5.1 has no &&, and cmd.exe doesn't take single
			// quotes; a double-quoted path and ; run in PowerShell. -LiteralPath,
			// because cd (Set-Location -Path) treats [ and ] as wildcards.
			return "Set-Location -LiteralPath " + powershellQuote(s.Cwd) + "; " + cmd
		}
		return "cd " + shellQuote(s.Cwd) + " && " + cmd
	case "codex":
		// Codex finds a session from any directory; a cd would only make the
		// resume fail if that directory had since been deleted.
		return "codex resume " + shellQuote(s.ID)
	default:
		return ""
	}
}

// Sessions returns the n most recently active sessions (all if n <= 0).
// Jots without a session id, such as those from jotaside add, are not in any.
func (s *Service) Sessions(ctx context.Context, n int) ([]Session, error) {
	all, err := s.store.List(ctx, "", 0)
	if err != nil {
		return nil, err
	}
	ss := groupSessions(all)
	if n > 0 && len(ss) > n {
		ss = ss[:n]
	}
	setStartDirs(ss)
	return ss, nil
}

// Find returns the sessions with a jot containing q, and the Claude Code
// sessions whose transcripts acted on it (see scan.go), most recently active
// first, each with its matching jots, plus the matching jots that have no
// session. A session found both ways is listed once. Matching is the same as
// Search, except that a query shaped like a ticket key (SUP-4821) only matches
// it as a whole word, so it doesn't find SUP-48210.
func (s *Service) Find(ctx context.Context, q string) ([]Session, []Entry, error) {
	found, err := s.FindEach(ctx, []string{q})
	if err != nil {
		return nil, nil, err
	}
	return found[0].Sessions, found[0].Loose, nil
}

// Found is what Find returns for one query.
type Found struct {
	Sessions []Session
	Loose    []Entry
}

// FindEach runs Find for each query, reading the transcripts once for all of
// them. found[i] is for qs[i].
func (s *Service) FindEach(ctx context.Context, qs []string) ([]Found, error) {
	ms := make([]queryMatcher, len(qs))
	for i, q := range qs {
		if strings.TrimSpace(q) == "" {
			return nil, errors.New("empty search query")
		}
		ms[i] = newQueryMatcher(q)
	}
	all, err := s.store.List(ctx, "", 0)
	if err != nil {
		return nil, err
	}
	groups := groupSessions(all)
	scanned, err := scanTranscripts(ctx, s.transcripts, ms)
	if err != nil {
		return nil, err
	}
	starts := map[sessionKey]string{} // start directories already read
	out := make([]Found, len(qs))
	for i, m := range ms {
		ss, loose, err := s.findOne(ctx, m, groups, scanned[i], starts)
		if err != nil {
			return nil, err
		}
		out[i] = Found{ss, loose}
	}
	return out, nil
}

// findOne is Find for one query, given every session with jots and the
// sessions the scan found for it.
func (s *Service) findOne(ctx context.Context, m queryMatcher, groups []Session, scanned []scannedSession, starts map[sessionKey]string) ([]Session, []Entry, error) {
	matches, err := s.store.Search(ctx, m.query, 0)
	if err != nil {
		return nil, nil, err
	}
	if m.whole != nil {
		kept := matches[:0]
		for _, e := range matches {
			if m.whole.MatchString(e.Text) || m.whole.MatchString(e.DoneNote) {
				kept = append(kept, e)
			}
		}
		matches = kept
	}
	var loose []Entry
	for _, e := range matches { // newest first, as Search returns them
		if e.SessionID == "" {
			loose = append(loose, e)
		}
	}
	byKey := map[sessionKey][]Entry{}
	for i := len(matches) - 1; i >= 0; i-- { // oldest first within a session
		if e := matches[i]; e.SessionID != "" {
			byKey[keyOf(e)] = append(byKey[keyOf(e)], e)
		}
	}
	fromScan := map[sessionKey]scannedSession{}
	for _, x := range scanned {
		fromScan[sessionKey{"claude", x.id}] = x
	}
	var found []Session
	for _, ss := range groups {
		k := sessionKey{ss.Source, ss.ID}
		jm, jotted := byKey[k]
		x, inScan := fromScan[k]
		if !jotted && !inScan {
			continue
		}
		ss.Matches = jm
		if x.lastActive.After(ss.LastActive) {
			ss.LastActive = x.lastActive
		}
		delete(fromScan, k)
		found = append(found, ss)
	}
	for i := range found {
		k := sessionKey{found[i].Source, found[i].ID}
		if dir, ok := starts[k]; ok {
			if dir != "" {
				found[i].Cwd = dir
			}
			continue
		}
		before := found[i].Cwd
		setStartDirs(found[i : i+1])
		if found[i].Cwd != before {
			starts[k] = found[i].Cwd
		} else {
			starts[k] = ""
		}
	}
	for _, x := range scanned {
		if _, ok := fromScan[sessionKey{"claude", x.id}]; !ok {
			continue // listed with its jots
		}
		ss := ClaudeSession(x.path)
		ss.LastActive = x.lastActive
		found = append(found, ss)
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].LastActive.After(found[j].LastActive) })
	return found, loose, nil
}

// queryMatcher is find's matching rule for one query.
type queryMatcher struct {
	// query is the query as searched: a ticket key without stray spaces.
	query string
	// whole matches a ticket-shaped query as a whole word; nil for any other.
	whole *regexp.Regexp
	// lower is the query in lowercase ASCII, for a substring match.
	lower string
	// needle is what the transcript prefilter looks for, lowercase: the longest
	// part of the query that JSON writes as is.
	needle []byte
}

func newQueryMatcher(q string) queryMatcher {
	// A key may have stray spaces around it; any other query is taken as typed.
	if key := strings.TrimSpace(q); ticketKeyShape.MatchString(key) {
		q = key
	}
	m := queryMatcher{query: q, lower: asciiLower(q)}
	if ticketKeyShape.MatchString(q) {
		// Not \b: it counts "_" as part of a word, which would miss
		// SUP-4821_cache.
		m.whole = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])` + regexp.QuoteMeta(q) + `(?:[^A-Za-z0-9]|$)`)
	}
	// JSON escapes quotes, backslashes and control characters, so the bytes of
	// a query with them aren't in the file; look for the longest run without.
	for _, part := range strings.FieldsFunc(m.lower, func(r rune) bool { return r == '"' || r == '\\' || r < 0x20 || r == 0x7f }) {
		if len(part) > len(m.needle) {
			m.needle = []byte(part)
		}
	}
	return m
}

// match reports whether text matches the query.
func (m queryMatcher) match(text string) bool {
	if m.whole != nil {
		return m.whole.MatchString(text)
	}
	return containsFold(text, m.lower)
}

// asciiLower lowercases the ASCII letters in s.
func asciiLower(s string) string {
	b := []byte(s)
	lowerASCII(b)
	return string(b)
}

// setStartDirs sets each Claude Code session's Cwd to the directory it
// started in. A jot's cwd is wherever the agent was when it was captured,
// which may be a directory it moved to later.
func setStartDirs(ss []Session) {
	for i := range ss {
		if ss[i].Source != "claude" {
			continue
		}
		for _, e := range ss[i].Jots {
			var meta struct {
				TranscriptPath string `json:"transcript_path"`
			}
			if json.Unmarshal(e.Metadata, &meta) != nil || meta.TranscriptPath == "" {
				continue
			}
			if dir := transcriptStartDir(meta.TranscriptPath); dir != "" {
				ss[i].Cwd = dir
				break
			}
		}
	}
}

// ClaudeSession returns the Claude Code session whose transcript is at path,
// with the directory it started in, for a resume command when the session
// has no jot. The ID is the transcript's file name.
func ClaudeSession(path string) Session {
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	return Session{Source: "claude", ID: id, Cwd: transcriptStartDir(path)}
}

// transcriptStartDir returns the first cwd recorded in a Claude Code
// transcript (JSON lines), or "" if there is none in its first records.
func transcriptStartDir(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for range 100 {
		line, err := r.ReadBytes('\n')
		var rec struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(line, &rec) == nil && rec.Cwd != "" {
			return rec.Cwd
		}
		if err != nil {
			return ""
		}
	}
	return ""
}

// ticketKeyShape is a query that looks like a ticket key. Which prefixes are
// really tickets isn't known (there is no allowlist), so UTF-8 is treated the
// same: it matches as a whole word too.
var ticketKeyShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[0-9]+$`)

type sessionKey struct{ source, id string }

func keyOf(e Entry) sessionKey { return sessionKey{e.Source, e.SessionID} }

// groupSessions groups entries (any order) by source and session id, most
// recently active session first.
func groupSessions(es []Entry) []Session {
	sorted := append([]Entry(nil), es...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	idx := map[sessionKey]int{}
	var ss []Session
	for _, e := range sorted {
		if e.SessionID == "" {
			continue
		}
		k := keyOf(e)
		i, ok := idx[k]
		if !ok {
			i = len(ss)
			idx[k] = i
			ss = append(ss, Session{Source: e.Source, ID: e.SessionID})
		}
		ss[i].Jots = append(ss[i].Jots, e)
		if ss[i].Cwd == "" {
			ss[i].Cwd = e.Cwd
		}
	}
	for i := range ss {
		ss[i].LastActive = ss[i].Latest().CreatedAt
		ss[i].Label = ss[i].Jots[0]
		for _, e := range ss[i].Jots {
			if _, ok := sessionName(e.Text); ok {
				ss[i].Label = e
			}
		}
	}
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].Latest().ID > ss[j].Latest().ID })
	return ss
}

// sessionName returns the name a "session:" jot (any case) gives its session:
// the first line of the text after the prefix, which may start on the next
// line. It reports false if text has no such prefix or names nothing, so a
// bare "session:" never blanks out a label.
func sessionName(text string) (string, bool) {
	if len(text) < len(SessionPrefix) || !strings.EqualFold(text[:len(SessionPrefix)], SessionPrefix) {
		return "", false
	}
	name, _, _ := strings.Cut(strings.TrimSpace(text[len(SessionPrefix):]), "\n")
	name = strings.TrimSpace(name)
	return name, name != ""
}

// powershellQuote double-quotes s for PowerShell, which escapes with a backtick.
func powershellQuote(s string) string {
	r := strings.NewReplacer("`", "``", `"`, "`\"", "$", "`$")
	return `"` + r.Replace(s) + `"`
}

// shellQuote quotes s for a POSIX shell unless it only has safe characters.
func shellQuote(s string) string {
	safe := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:@%+=,", r)
	}
	if s != "" && strings.IndexFunc(s, func(r rune) bool { return !safe(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
