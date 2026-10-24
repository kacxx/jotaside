package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Open links take a user back to the chat a jot came from. They are built
// from ids that come from hook payloads and Claude Desktop's own files, so an
// id is checked before it is put in a link, and a link is only built for the
// routes that were tested:
//
//   - Codex app: codex://threads/<session id> (documented).
//   - Claude Desktop, when the session's entrypoint is exactly
//     "claude-desktop": claude://code/continue?session=local_<uuid>
//     (undocumented; see docs/decisions.md). Desktop's id is found in its
//     session file, which also says whether the session is archived.
//
// Anything else has no link, and the caller shows the resume command.
var (
	uuidShape         = regexp.MustCompile(`^(?i:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
	desktopIDShape    = regexp.MustCompile(`^local_(?i:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
	cliSessionKey     = regexp.MustCompile(`"cliSessionId"\s*:\s*"([^"\\]*)"`)
	isArchivedKey     = regexp.MustCompile(`"isArchived"\s*:\s*(true|false)`)
	desktopEntrypoint = "claude-desktop"

	// The only links jotaside open will hand to the system.
	openableClaude = regexp.MustCompile(`^claude://code/continue\?session=local_(?i:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
	openableCodex  = regexp.MustCompile(`^codex://threads/(?i:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
)

// OpenableURL reports whether u is a link jotaside open may open: exactly a
// Claude Desktop continue link or a Codex thread link, with a UUID id.
func OpenableURL(u string) bool {
	return openableClaude.MatchString(u) || openableCodex.MatchString(u)
}

// OpenCommand returns the shell command that opens jot id's chat with the
// jotaside at exe. Agents run it from a shell that may not have jotaside on its
// PATH, so it uses the full path.
func OpenCommand(exe string, id int64) string {
	return shellQuote(exe) + " open " + strconv.FormatInt(id, 10)
}

// DoneCommand returns the shell command, with the full path to jotaside at exe
// for the same reason as OpenCommand, that closes jots. exe may be "" when
// the path is unknown, which gives a bare jotaside.
func DoneCommand(exe string) string {
	if exe == "" {
		exe = "jotaside"
	}
	return shellQuote(exe) + ` done <id>... [--note "why"]`
}

// ResumeFor returns the command that reopens the session e belongs to, or ""
// if e has no session or its agent has no resume command.
func ResumeFor(e Entry) string {
	ss := groupSessions([]Entry{e})
	if len(ss) == 0 {
		return ""
	}
	setStartDirs(ss)
	return ss[0].ResumeCommand()
}

// headerSize is how much of a Desktop session file is read to find its keys.
// Both are within the first ~1.1 KB of every file seen; the files are
// otherwise hundreds of KB of settings jotaside has no need to read.
const headerSize = 4096

// linkRef is what a jot (or a session's jots) says about where it came from.
type linkRef struct {
	source, sessionID string
	desktopHint       string // claude_desktop_session_id from the hook, unchecked
	entrypoint        string
	transcript        string
	oldest, newest    time.Time // when the session's jots were captured
}

// want is what find needs to look a Claude session up.
type want struct {
	hint  string    // Desktop id the hook recorded, unchecked
	since time.Time // the session file can't be older than its oldest jot
	fresh bool      // a jot is recent: a miss may only mean the file isn't written yet
}

// missTTL is how long a miss is remembered, and how recent a jot must be to
// not be remembered as one at all.
const missTTL = 10 * time.Minute

// scanSlack is subtracted from a jot's time before it is used as the point
// the scan stops at, to allow for clocks and file system timestamps.
const scanSlack = time.Minute

type desktopSession struct {
	localID  string
	archived bool
}

// desktopLinks finds Claude Desktop's session files. misses lasts as long as
// the Service, so a deleted session doesn't cost a full scan on every call.
type desktopLinks struct {
	mu     sync.Mutex
	dir    string
	misses map[string]time.Time // Claude session id -> when it was missed
	now    func() time.Time
}

// SetDesktopSessionsDir sets the directory holding Claude Desktop's session
// files (<dir>/*/*/local_*.json). The default is macOS's or Windows's; elsewhere it is
// empty and Claude Desktop jots get no link.
func (s *Service) SetDesktopSessionsDir(dir string) {
	s.desktop.mu.Lock()
	defer s.desktop.mu.Unlock()
	s.desktop.dir = dir
}

// CanOpen reports whether jotaside open can run on goos: the two systems whose
// default handler for claude:// and codex:// links has been checked.
func CanOpen(goos string) bool { return goos == "darwin" || goos == "windows" }

func defaultDesktopDir() string {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions")
	case "windows":
		// The MSIX build mirrors this folder under
		// %LOCALAPPDATA%\Packages\Claude_*\LocalCache\Roaming; the Roaming
		// copy is the one with the same files.
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return ""
		}
		return filepath.Join(appData, "Claude", "claude-code-sessions")
	}
	return ""
}

// AddOpenURLs sets OpenURL on each entry that has one. The link belongs to the
// jot's session, so it is built from what all the session's jots record (a jot
// from before the Desktop id was stored still gets its session's link), and
// every jot of a session gets the same one. It looks Claude Desktop's session
// files up only if an entry came from there.
func (s *Service) AddOpenURLs(ctx context.Context, es []Entry) {
	byKey := map[sessionKey][]Entry{}
	var ids []string
	for _, e := range es {
		if e.SessionID != "" {
			if _, ok := byKey[keyOf(e)]; !ok {
				ids = append(ids, e.SessionID)
			}
			byKey[keyOf(e)] = nil
		}
	}
	if len(ids) > 0 {
		if all, err := s.store.BySessions(ctx, ids); err == nil {
			for _, e := range all {
				if _, ok := byKey[keyOf(e)]; ok {
					byKey[keyOf(e)] = append(byKey[keyOf(e)], e)
				}
			}
		}
	}
	refs := make([]linkRef, len(es))
	for i, e := range es {
		jots := byKey[keyOf(e)]
		if e.SessionID == "" || len(jots) == 0 {
			jots = []Entry{e}
		}
		sort.Slice(jots, func(a, b int) bool { return jots[a].ID > jots[b].ID }) // newest first
		refs[i] = refFor(jots)
	}
	for i, u := range s.openURLs(refs) {
		es[i].OpenURL = u
	}
}

// AddSessionOpenURLs sets OpenURL on each session. It is separate from Find so
// callers pay for the lookup only for the sessions they show.
func (s *Service) AddSessionOpenURLs(ss []Session) {
	refs := make([]linkRef, len(ss))
	for i, x := range ss {
		newestFirst := make([]Entry, len(x.Jots))
		for j, e := range x.Jots {
			newestFirst[len(x.Jots)-1-j] = e
		}
		refs[i] = refFor(newestFirst)
	}
	for i, u := range s.openURLs(refs) {
		ss[i].OpenURL = u
	}
}

// refFor merges what es (newest first) record about one session, taking each
// field from the newest jot that has it.
func refFor(es []Entry) linkRef {
	var r linkRef
	for _, e := range es {
		r.source, r.sessionID = e.Source, e.SessionID
		if r.oldest.IsZero() || e.CreatedAt.Before(r.oldest) {
			r.oldest = e.CreatedAt
		}
		if e.CreatedAt.After(r.newest) {
			r.newest = e.CreatedAt
		}
		var m struct {
			Transcript string `json:"transcript_path"`
			Desktop    string `json:"claude_desktop_session_id"`
			Entrypoint string `json:"claude_entrypoint"`
		}
		if json.Unmarshal(e.Metadata, &m) != nil {
			continue
		}
		if r.desktopHint == "" {
			r.desktopHint = m.Desktop
		}
		if r.entrypoint == "" {
			r.entrypoint = m.Entrypoint
		}
		if r.transcript == "" {
			r.transcript = m.Transcript
		}
	}
	return r
}

func (s *Service) openURLs(refs []linkRef) []string {
	out := make([]string, len(refs))
	wants := map[string]want{}
	hasDir := s.desktop.hasDir() // no transcript is read where there are no session files
	for i, r := range refs {
		switch {
		case !uuidShape.MatchString(r.sessionID):
		case r.source == "codex":
			out[i] = "codex://threads/" + url.PathEscape(r.sessionID)
		case r.source == "claude" && hasDir && claudeEntrypoint(r) == desktopEntrypoint:
			w := wants[r.sessionID]
			if w.hint == "" {
				w.hint = r.desktopHint
			}
			if w.since.IsZero() || r.oldest.Before(w.since) {
				w.since = r.oldest
			}
			w.fresh = w.fresh || s.desktop.clock().Sub(r.newest) < missTTL
			wants[r.sessionID] = w
		}
	}
	found := s.desktop.find(wants)
	for i, r := range refs {
		if d, ok := found[r.sessionID]; ok && out[i] == "" && !d.archived {
			out[i] = "claude://code/continue?session=" + url.QueryEscape(d.localID)
		}
	}
	return out
}

// claudeEntrypoint is the entrypoint the hook recorded or, for a jot captured
// before it was recorded, the one in the session's transcript.
func claudeEntrypoint(r linkRef) string {
	if r.entrypoint != "" || r.transcript == "" {
		return r.entrypoint
	}
	return transcriptEntrypoint(r.transcript)
}

// transcriptEntrypoint returns the first entrypoint recorded in a Claude Code
// transcript, or "" if there is none in its first records.
func transcriptEntrypoint(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for range 100 {
		line, err := r.ReadBytes('\n')
		var rec struct {
			Entrypoint string `json:"entrypoint"`
		}
		if json.Unmarshal(line, &rec) == nil && rec.Entrypoint != "" {
			return rec.Entrypoint
		}
		if err != nil {
			return ""
		}
	}
	return ""
}

func (d *desktopLinks) hasDir() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dir != ""
}

func (d *desktopLinks) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// find returns the Desktop session for each Claude session id in wants. A
// hint is checked against its file first, even for an id remembered as
// missing. The rest are looked for newest file first, stopping once all are
// found or once files are older than the oldest jot looked for: Desktop
// rewrites a session's file as the chat goes on, so the file can't predate a
// jot in it. An id not found is remembered as missing for missTTL, unless a
// jot of it is recent, since Desktop may not have written its file yet.
func (d *desktopLinks) find(wants map[string]want) map[string]desktopSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	found := map[string]desktopSession{}
	if len(wants) == 0 || d.dir == "" {
		return found
	}
	now := d.clock()
	files, _ := filepath.Glob(filepath.Join(d.dir, "*", "*", "local_*.json"))
	pending := map[string]want{}
	for id, w := range wants {
		if desktopIDShape.MatchString(w.hint) {
			for _, f := range files {
				if filepath.Base(f) != w.hint+".json" {
					continue
				}
				if cli, archived, ok := readHeader(f); ok && cli == id {
					found[id] = desktopSession{w.hint, archived}
				}
				break
			}
		}
		if _, ok := found[id]; ok {
			continue
		}
		if at, missed := d.misses[id]; missed && now.Sub(at) < missTTL {
			continue
		}
		pending[id] = w
	}
	if len(pending) > 0 {
		type file struct {
			path string
			mod  time.Time
		}
		byAge := make([]file, 0, len(files))
		for _, f := range files {
			if fi, err := os.Stat(f); err == nil {
				byAge = append(byAge, file{f, fi.ModTime()})
			}
		}
		sort.Slice(byAge, func(i, j int) bool { return byAge[i].mod.After(byAge[j].mod) })
		var stop time.Time // files older than this can't hold a pending session
		for _, w := range pending {
			if stop.IsZero() || w.since.Before(stop) {
				stop = w.since
			}
		}
		stop = stop.Add(-scanSlack)
		for _, f := range byAge {
			if len(pending) == 0 || f.mod.Before(stop) {
				break
			}
			cli, archived, ok := readHeader(f.path)
			local := filepath.Base(f.path[:len(f.path)-len(".json")])
			if _, want := pending[cli]; ok && want && desktopIDShape.MatchString(local) {
				found[cli] = desktopSession{local, archived}
				delete(pending, cli)
			}
		}
		if d.misses == nil {
			d.misses = map[string]time.Time{}
		}
		for id, w := range pending {
			if !w.fresh {
				d.misses[id] = now
			}
		}
	}
	return found
}

// readHeader returns a Desktop session file's cliSessionId and isArchived. It
// reads the first headerSize bytes, and the whole file only if either key is
// not in them.
func readHeader(path string) (cli string, archived, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, false
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, headerSize))
	c, a := cliSessionKey.FindSubmatch(b), isArchivedKey.FindSubmatch(b)
	if c == nil || a == nil {
		rest, _ := io.ReadAll(f)
		b = append(b, rest...)
		c, a = cliSessionKey.FindSubmatch(b), isArchivedKey.FindSubmatch(b)
	}
	if c == nil || a == nil {
		return "", false, false
	}
	return string(c[1]), bytes.Equal(a[1], []byte("true")), true
}
