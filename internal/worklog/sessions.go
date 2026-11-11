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
	// ID is the session's full ID; "" when a stamp matches no session.
	ID string
	// Label is the session's jotaside label, when it has jots.
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
	// Sessions lists jotaside's sessions, for a stamp with no Claude Code
	// transcript (a Cursor chat); nil skips it.
	Sessions func(ctx context.Context) ([]app.Session, error)

	listed bool
	all    []app.Session
}

// For returns the item's sessions: those stamped on it, then any others
// jotaside finds by the work items it names.
func (l *Lookup) For(ctx context.Context, it Item) []Ref {
	var refs []Ref
	seen := map[string]bool{}
	for _, stamp := range it.Stamps() {
		via := "chat:" + stamp
		found := false
		for _, p := range l.transcripts(stamp) {
			found = true
			if s := app.ClaudeSession(p); !seen[s.ID] {
				seen[s.ID] = true
				refs = append(refs, Ref{Via: via, ID: s.ID, Resume: s.ResumeCommand()})
			}
		}
		if found {
			continue
		}
		for _, s := range l.jotted(ctx) {
			if !strings.HasPrefix(strings.ToLower(s.ID), stamp) {
				continue
			}
			found = true
			if !seen[s.ID] {
				seen[s.ID] = true
				refs = append(refs, Ref{Via: via, ID: s.ID, Label: s.LabelText(), Resume: s.ResumeCommand()})
			}
		}
		if !found {
			refs = append(refs, Ref{Via: via})
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

// jotted returns jotaside's sessions, listed once.
func (l *Lookup) jotted(ctx context.Context) []app.Session {
	if !l.listed && l.Sessions != nil {
		l.all, _ = l.Sessions(ctx)
	}
	l.listed = true
	return l.all
}

// transcripts returns the transcripts of the sessions whose ID starts with
// stamp, which is hex and hyphens only (chatFact), so it is safe in a glob.
func (l *Lookup) transcripts(stamp string) []string {
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
