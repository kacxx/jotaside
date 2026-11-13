package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"regexp"

	"github.com/kacxx/jotaside/internal/capture"
	"github.com/kacxx/jotaside/internal/store"
)

// Work items a session's first prompt can name (#32). Each pattern's first
// group is where the item starts; boundaries are matched, not asserted, as Go
// has no lookbehind.
var (
	// A tracker key: uppercase letters and digits, a hyphen, digits, as a
	// whole word. No prefix allowlist, so SHA-256 matches too; a later
	// ">> session:" jot replaces a wrong label.
	trackerKey = regexp.MustCompile(`(?:^|[^A-Za-z0-9])([A-Z][A-Z0-9]*-[0-9]+)(?:[^A-Za-z0-9]|$)`)
	// owner/repo#N, as typed.
	repoRef = regexp.MustCompile(`(?:^|[^A-Za-z0-9._/-])([A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+#[0-9]+)(?:[^A-Za-z0-9_]|$)`)
	// A GitHub issue or pull request URL, labelled as owner/repo#N.
	githubURL = regexp.MustCompile(`(?:^|[^A-Za-z0-9])(https?://(?:www\.)?github\.com/([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9._-]+)/(?:issues|pull)/([0-9]+))(?:[^0-9]|$)`)
)

// WorkItem returns the first work item the prompt names: a tracker key
// (ABC-123), owner/repo#N, or a GitHub issue or PR URL as owner/repo#N.
// Leading <system-reminder> blocks are not searched.
func WorkItem(prompt string) (string, bool) {
	prompt = capture.StripSystemReminders(prompt)
	item, at := "", -1
	first := func(start int, s string) {
		if start >= 0 && (at < 0 || start < at) {
			item, at = s, start
		}
	}
	if m := repoRef.FindStringSubmatchIndex(prompt); m != nil {
		first(m[2], prompt[m[2]:m[3]])
	}
	if m := githubURL.FindStringSubmatchIndex(prompt); m != nil {
		first(m[2], prompt[m[4]:m[5]]+"/"+prompt[m[6]:m[7]]+"#"+prompt[m[8]:m[9]])
	}
	// Last, so a key-shaped owner (ABC-1/repo#2), which starts where the key
	// does, keeps the whole owner/repo#N.
	if m := trackerKey.FindStringSubmatchIndex(prompt); m != nil {
		first(m[2], prompt[m[2]:m[3]])
	}
	return item, at >= 0
}

// FirstPrompt reports whether a Claude Code session's prompt is its first:
// the transcript is missing or has no "user" record yet. Claude Code writes a
// prompt's user record only after the prompt hook lets it through. A
// transcript that can't be read counts as not first.
func FirstPrompt(transcriptPath string) bool {
	if transcriptPath == "" {
		return true
	}
	f, err := os.Open(transcriptPath)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"user"`)) {
			var rec struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(line, &rec) == nil && rec.Type == "user" {
				return false
			}
		}
		if err != nil {
			return errors.Is(err, io.EOF)
		}
	}
}

// LabelSession saves req as a done jot, the session's automatic label, unless
// req's session already has a "session:" jot with a name. It reports whether
// it saved one; if not, the entry is the existing label (or the entry
// req.OnceKey matched, when another hook saved the same prompt's label
// first), or empty when req has no session id.
func (s *Service) LabelSession(ctx context.Context, req CaptureRequest) (Entry, bool, error) {
	if req.SessionID == "" {
		return Entry{}, false, nil
	}
	es, err := s.store.BySessions(ctx, []string{req.SessionID})
	if err != nil {
		return Entry{}, false, err
	}
	for _, e := range es {
		if _, ok := sessionName(e.Text); ok && e.Source == req.Source {
			return e, false, nil
		}
	}
	req.Status = store.StatusDone
	e, r, err := s.CaptureOnce(ctx, req)
	if err != nil {
		return Entry{}, false, err
	}
	return e, r == Saved, nil
}
