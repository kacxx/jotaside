package worklog

import (
	"context"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kacxx/jotaside/internal/app"
)

// Ref is one session found for an item.
type Ref struct {
	// Via is how it was found: "chat:<stamp>" for a session stamped on the
	// item, else the work-item key jotaside found it by.
	Via string
	// ID is the session's full ID; "" when a stamp's transcript is gone.
	ID string
	// Label is the jotaside label of a session found by key.
	Label string
	// Resume reopens the session; "" when there is no way to.
	Resume string
}

// Lookup finds an item's sessions.
type Lookup struct {
	// Transcripts is Claude Code's projects directory, holding
	// <project>/<session id>.jsonl.
	Transcripts string
	// Find is jotaside's find, for the key fallback; nil skips it.
	Find func(ctx context.Context, q string) ([]app.Session, []app.Entry, error)
}

// For returns the item's sessions: those stamped on it, then any others
// jotaside finds by the work items it names.
func (l Lookup) For(ctx context.Context, it Item) []Ref {
	var refs []Ref
	seen := map[string]bool{}
	for _, stamp := range it.Stamps() {
		paths := l.transcripts(stamp)
		if len(paths) == 0 {
			refs = append(refs, Ref{Via: "chat:" + stamp})
			continue
		}
		for _, p := range paths {
			s := app.ClaudeSession(p)
			if seen[s.ID] {
				continue
			}
			seen[s.ID] = true
			refs = append(refs, Ref{Via: "chat:" + stamp, ID: s.ID, Resume: s.ResumeCommand()})
		}
	}
	if l.Find == nil {
		return refs
	}
	for _, key := range it.Keys() {
		found, _, err := l.Find(ctx, key)
		if err != nil {
			continue
		}
		for _, s := range found {
			if seen[s.ID] {
				continue
			}
			seen[s.ID] = true
			refs = append(refs, Ref{Via: key, ID: s.ID, Label: s.LabelText(), Resume: s.ResumeCommand()})
		}
	}
	return refs
}

// transcripts returns the transcripts of the sessions whose ID starts with
// stamp, which is hex and hyphens only (chatToken), so it is safe in a glob.
func (l Lookup) transcripts(stamp string) []string {
	if l.Transcripts == "" {
		return nil
	}
	paths, err := filepath.Glob(filepath.Join(l.Transcripts, "*", stamp+"*.jsonl"))
	if err != nil {
		return nil
	}
	kept := paths[:0]
	for _, p := range paths {
		if strings.HasPrefix(strings.ToLower(filepath.Base(p)), stamp) {
			kept = append(kept, p)
		}
	}
	sort.Strings(kept)
	return kept
}
