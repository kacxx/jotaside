package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kacxx/jotaside/internal/app"
	"github.com/kacxx/jotaside/internal/store"
)

const codexJot = ">> retry budget in the payments client looks unbounded, check before release"

func runCodex(t *testing.T, payload []byte, open app.Opener) (string, *CodexBlock) {
	t.Helper()
	var out bytes.Buffer
	err := Codex(context.Background(), bytes.NewReader(payload), &out, open)
	var exit *BlockExit
	if err != nil && !errors.As(err, &exit) {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		if exit != nil {
			t.Fatalf("BlockExit %q with no JSON", exit.Reason)
		}
		return "", nil
	}
	s := out.String()
	var b CodexBlock
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		t.Fatalf("invalid Codex output %q: %v", s, err)
	}
	if exit == nil || exit.Reason != b.Reason {
		t.Fatalf("a block must return BlockExit with its reason %q; got %v", b.Reason, err)
	}
	return s, &b
}

func TestCodexPassThrough(t *testing.T) {
	for _, name := range []string{"normal", "mid_prompt", "malformed"} {
		t.Run(name, func(t *testing.T) {
			// Codex adds hook stdout to the model's context, so nothing at all.
			if out, _ := runCodex(t, fixture(t, "codex", name, nil), neverOpen(t)); out != "" {
				t.Fatalf("pass-through must write nothing, got %q", out)
			}
		})
	}
	for name, payload := range map[string]string{
		"empty stdin":       "",
		"wrong prompt type": `{"prompt": 42}`,
		"other event":       `{"hook_event_name":"Stop","prompt":">> x"}`,
		"not an object":     `[">> x"]`,
	} {
		t.Run(name, func(t *testing.T) {
			if out, _ := runCodex(t, []byte(payload), neverOpen(t)); out != "" {
				t.Fatalf("got %q", out)
			}
		})
	}
}

func TestCodexCapture(t *testing.T) {
	repo := gitRepo(t)
	open, path := tempDB(t)
	out, _ := runCodex(t, fixture(t, "codex", "capture", map[string]any{"cwd": repo}), open)
	// Exactly Codex's documented block shape: no Claude-only fields.
	if want := `{"decision":"block","reason":"✓ Jotted #1"}` + "\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}

	es := entries(t, path)
	if len(es) != 1 {
		t.Fatalf("entries: %d", len(es))
	}
	e := es[0]
	if e.Text != strings.TrimPrefix(codexJot, ">> ") || e.Source != "codex" ||
		e.SessionID != "0199a2c4-5e7b-7f10-8d3a-6b2e9c41f0d8" || e.Cwd != repo ||
		e.RepoName != filepath.Base(repo) || e.Branch != "feature/jot" || e.CommitSHA == "" {
		t.Fatalf("entry: %+v", e)
	}
	var meta map[string]any
	if err := json.Unmarshal(e.Metadata, &meta); err != nil {
		t.Fatalf("metadata %s: %v", e.Metadata, err)
	}
	want := map[string]any{
		"turn_id":                "0199a2c5-1f22-7c8e-a4b0-93d1e57a2c6f",
		"model":                  "gpt-5-codex",
		store.MetaTranscriptPath: "/Users/dev/.codex/sessions/2026/09/29/rollout-2026-09-29T10-14-03-0199a2c4-5e7b-7f10-8d3a-6b2e9c41f0d8.jsonl",
	}
	if len(meta) != len(want) || meta["turn_id"] != want["turn_id"] || meta["model"] != want["model"] ||
		meta[store.MetaTranscriptPath] != want[store.MetaTranscriptPath] {
		t.Fatalf("metadata = %v, want %v", meta, want)
	}
}

func TestCodexCaptureStoresTranscriptPath(t *testing.T) {
	open, path := tempDB(t)
	const rollout = "/Users/dev/.codex/sessions/2026/10/02/rollout-2026-10-02T09-00-00-0199a2c4.jsonl"
	runCodex(t, fixture(t, "codex", "capture", map[string]any{"transcript_path": rollout}), open)
	var meta map[string]any
	if e := entries(t, path)[0]; json.Unmarshal(e.Metadata, &meta) != nil || meta[store.MetaTranscriptPath] != rollout {
		t.Fatalf("metadata = %s, want transcript_path %q", e.Metadata, rollout)
	}
}

func TestCodexCaptureMinimalPayload(t *testing.T) {
	open, path := tempDB(t)
	_, b := runCodex(t, []byte(`{"prompt":">> bare","transcript_path":null}`), open)
	if b == nil || b.Reason != "✓ Jotted #1" {
		t.Fatalf("got %+v", b)
	}
	// With no turn_id or model there is no metadata; the store reads an empty
	// object back as nil.
	if e := entries(t, path)[0]; e.Source != "codex" || e.Text != "bare" || len(e.Metadata) != 0 {
		t.Fatalf("entry: %+v metadata=%s", e, e.Metadata)
	}
}

func TestCodexFailureBlocks(t *testing.T) {
	out, b := runCodex(t, fixture(t, "codex", "capture", nil), func() (*app.Service, error) {
		return nil, errors.New("disk on fire")
	})
	if b == nil || b.Decision != "block" {
		t.Fatalf("a recognised jot must be blocked even on failure: %q", out)
	}
	assertBlockedFailed(t, b.Reason, codexJot)
}

func TestCodexWrongTypedFieldsStillBlock(t *testing.T) {
	for name, override := range map[string]map[string]any{
		"session_id":      {"session_id": 123},
		"cwd":             {"cwd": []int{1}},
		"turn_id":         {"turn_id": false},
		"model":           {"model": map[string]any{"slug": "x"}},
		"hook_event_name": {"hook_event_name": 5},
		"transcript_path": {"transcript_path": 9},
	} {
		t.Run(name, func(t *testing.T) {
			open, path := tempDB(t)
			if _, b := runCodex(t, fixture(t, "codex", "capture", override), open); b == nil || b.Reason != "✓ Jotted #1" {
				t.Fatalf("got %+v", b)
			}
			if e := entries(t, path)[0]; e.Source != "codex" || e.Text != strings.TrimPrefix(codexJot, ">> ") {
				t.Fatalf("entry: %+v", e)
			}
		})
	}
}

func TestCodexOversized(t *testing.T) {
	smallLimit(t)
	pad := padString()
	if _, b := runCodex(t, big(t, `"model":"m"`, pad, `"prompt":">> secret"`), neverOpen(t)); b == nil ||
		!strings.Contains(b.Reason, "NOT saved") || !strings.Contains(b.Reason, ">> secret") {
		t.Fatalf("oversized jot must be blocked as NOT saved, got %+v", b)
	}
	if out, _ := runCodex(t, big(t, `"prompt":"fix the bug"`, pad), neverOpen(t)); out != "" {
		t.Fatalf("oversized ordinary prompt: %q", out)
	}
	if out, _ := runCodex(t, big(t, `"hook_event_name":"Stop"`, `"prompt":">> secret"`, pad), neverOpen(t)); out != "" {
		t.Fatalf("oversized other event: %q", out)
	}
}

func TestCodexRepeatedJotSavedOnce(t *testing.T) {
	open, path := tempDB(t)
	payload := fixture(t, "codex", "capture", map[string]any{"cwd": t.TempDir()})
	_, a := runCodex(t, payload, open)
	_, b := runCodex(t, payload, open)
	if a == nil || a.Reason != "✓ Jotted #1" || b == nil || b.Reason != "✓ Already jotted #1" {
		t.Fatalf("replies: %+v, %+v", a, b)
	}
	if es := entries(t, path); len(es) != 1 {
		t.Fatalf("saved %d entries, want 1", len(es))
	}
}
