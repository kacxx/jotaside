package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
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
}

// Latest returns the session's newest jot.
func (s Session) Latest() Entry { return s.Jots[len(s.Jots)-1] }

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

// Find returns the sessions with a jot containing q, most recently active
// first, each with its matching jots, plus the matching jots that have no
// session. Matching is the same as Search, except that a query shaped like a
// ticket key (SUP-4821) only matches it as a whole word, so it doesn't find
// SUP-48210.
func (s *Service) Find(ctx context.Context, q string) ([]Session, []Entry, error) {
	if strings.TrimSpace(q) == "" {
		return nil, nil, errors.New("empty search query")
	}
	// A key may have stray spaces around it; any other query is taken as typed.
	if key := strings.TrimSpace(q); ticketKeyShape.MatchString(key) {
		q = key
	}
	matches, err := s.store.Search(ctx, q, 0)
	if err != nil {
		return nil, nil, err
	}
	if ticketKeyShape.MatchString(q) {
		// Not \b: it counts "_" as part of a word, which would miss
		// SUP-4821_cache.
		whole := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])` + regexp.QuoteMeta(q) + `(?:[^A-Za-z0-9]|$)`)
		kept := matches[:0]
		for _, e := range matches {
			if whole.MatchString(e.Text) || whole.MatchString(e.DoneNote) {
				kept = append(kept, e)
			}
		}
		matches = kept
	}
	all, err := s.store.List(ctx, "", 0)
	if err != nil {
		return nil, nil, err
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
	var found []Session
	for _, ss := range groupSessions(all) {
		if m, ok := byKey[sessionKey{ss.Source, ss.ID}]; ok {
			ss.Matches = m
			found = append(found, ss)
		}
	}
	setStartDirs(found)
	return found, loose, nil
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
