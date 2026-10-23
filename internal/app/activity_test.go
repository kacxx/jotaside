package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var activityJotAt = time.Date(2026, 10, 12, 2, 16, 0, 0, time.UTC)

// writeTranscript writes inline JSONL records to a temp file and returns a
// Claude jot, captured at activityJotAt, that points at it.
func writeTranscript(t *testing.T, lines ...string) Entry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]string{"transcript_path": path})
	return Entry{ID: 31, Source: "claude", SessionID: "s1", CreatedAt: activityJotAt, Metadata: meta}
}

func userLine(ts, text string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":%q}}`, ts, text)
}

func assistantLine(ts, text string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, ts, text)
}

func toolLine(ts, name string) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","id":"t","name":%q}]}}`, ts, name)
}

func TestActivityAfterTheJot(t *testing.T) {
	e := writeTranscript(t,
		userLine("2026-10-12T02:10:00Z", "an earlier prompt"),
		assistantLine("2026-10-12T02:10:01Z", "An earlier answer."),
		userLine("2026-10-12T02:17:00Z", "can you print out all these PRs URLs"),
		toolLine("2026-10-12T02:17:01Z", "Bash"),
		toolLine("2026-10-12T02:17:02Z", "Bash"),
		toolLine("2026-10-12T02:17:03Z", "Read"),
		`{"type":"user","timestamp":"2026-10-12T02:17:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"output"}]}}`,
		assistantLine("2026-10-12T02:17:05Z", "Here are the four PRs."),
		`{"type":"assistant","isSidechain":true,"timestamp":"2026-10-12T02:17:06Z","message":{"role":"assistant","content":[{"type":"text","text":"subagent chatter"}]}}`,
		`{"type":"user","isMeta":true,"timestamp":"2026-10-12T02:17:07Z","message":{"role":"user","content":"meta"}}`,
		`{"type":"user","timestamp":"2026-10-12T02:17:08Z","message":{"role":"user","content":">> a later jot"}}`,
		`not json`,
		userLine("2026-10-12T02:18:00Z", "I've added review comments to those PRs"),
	)
	a := activityFor(e)
	if a.Unavailable != "" || a.Omitted != 0 || a.ReadLimited {
		t.Fatalf("activity: %+v", a)
	}
	var got []string
	for _, r := range a.Records {
		got = append(got, r.Role+": "+r.Text)
	}
	want := []string{
		"user: can you print out all these PRs URLs",
		"tools: 3 tool calls: Bash ×2, Read",
		"assistant: Here are the four PRs.",
		"user: I've added review comments to those PRs",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("records:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !a.Records[1].At.Equal(time.Date(2026, 10, 12, 2, 17, 1, 0, time.UTC)) {
		t.Fatalf("tool group time = %v", a.Records[1].At)
	}
}

func TestActivityRemovesTerminalEscapes(t *testing.T) {
	// The helpers quote with %q, which writes \x1b and \a, not JSON's \u escapes.
	j := strings.NewReplacer(`\x1b`, `\u001b`, `\a`, `\u0007`).Replace
	a := activityFor(writeTranscript(t,
		j(userLine("2026-10-12T02:17:00Z", "check \x1b]0;pwned\athis\rnow")),
		j(toolLine("2026-10-12T02:17:01Z", "Ba\x1b[2Ksh")),
		j(assistantLine("2026-10-12T02:17:02Z", "visible\x1b[2K\r\x1b[1Ahidden\nsee \x1b]8;;https://evil.example\x1b\\the docs\x1b]8;;\x1b\\\n\tdone")),
		j(assistantLine("2026-10-12T02:17:03Z", "\x1b[2J\r")),
	))
	var got []string
	for _, r := range a.Records {
		got = append(got, r.Role+": "+r.Text)
	}
	want := []string{
		"user: check this now",
		"tools: 1 tool call: Bash",
		"assistant: visible hidden\nsee the docs\n\tdone",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("records:\n%q\nwant:\n%q", got, want)
	}
}

func TestActivityNothingAfterTheJot(t *testing.T) {
	a := activityFor(writeTranscript(t, userLine("2026-10-12T02:10:00Z", "before")))
	if a.Unavailable != "" || len(a.Records) != 0 || a.Omitted != 0 {
		t.Fatalf("activity: %+v", a)
	}
}

func TestActivityCapsTextAndCountsWhatItLeftOut(t *testing.T) {
	long := strings.Repeat("x", 5000)
	lines := []string{}
	for i := range 10 {
		lines = append(lines, assistantLine(fmt.Sprintf("2026-10-12T02:%02d:00Z", 20+i), long))
	}
	a := activityFor(writeTranscript(t, lines...))
	total := 0
	for _, r := range a.Records {
		if n := len([]rune(r.Text)); n > maxActivityRecordRunes {
			t.Fatalf("record of %d runes", n)
		} else {
			total += n
		}
	}
	if total > maxActivityRunes || len(a.Records) < 2 || a.Omitted != 10-len(a.Records) {
		t.Fatalf("kept %d records (%d runes), omitted %d", len(a.Records), total, a.Omitted)
	}
	if !strings.HasSuffix(a.Records[0].Text, "…") {
		t.Fatalf("an over-long record is cut with an ellipsis: %q", a.Records[0].Text[len(a.Records[0].Text)-5:])
	}
}

func TestActivityReadIsBounded(t *testing.T) {
	defer func(read int64, line int) { maxActivityReadBytes, maxActivityLineBytes = read, line }(maxActivityReadBytes, maxActivityLineBytes)
	maxActivityReadBytes, maxActivityLineBytes = 3000, 1000

	big := assistantLine("2026-10-12T02:20:00Z", strings.Repeat("y", 1400)) // longer than a line may be
	lines := []string{big}
	for i := range 40 {
		lines = append(lines, assistantLine(fmt.Sprintf("2026-10-12T02:%02d:00Z", 21+i%30), fmt.Sprintf("reply %d", i)))
	}
	a := activityFor(writeTranscript(t, lines...))
	if !a.ReadLimited {
		t.Fatalf("reading should have stopped at the size limit: %+v", a)
	}
	if len(a.Records) == 0 || len(a.Records) >= 40 || a.Records[0].Text != "reply 0" {
		t.Fatalf("the oversized line is skipped and reading stops early: %d records, first %q", len(a.Records), a.Records[0].Text)
	}
}

func TestActivityUnavailable(t *testing.T) {
	meta := func(path string) json.RawMessage {
		b, _ := json.Marshal(map[string]string{"transcript_path": path})
		return b
	}
	for _, tc := range []struct {
		name   string
		e      Entry
		reason string
	}{
		{"aside add", Entry{ID: 1, Source: "cli"}, "not captured in an agent session"},
		{"cursor", Entry{ID: 2, Source: "cursor", SessionID: "c"}, "Cursor jots have no transcript"},
		{"codex with a path", Entry{ID: 3, Source: "codex", SessionID: "x", Metadata: meta("/does/not/matter")}, "Codex transcripts are not read yet"},
		{"claude without a path", Entry{ID: 4, Source: "claude", SessionID: "s"}, "did not record a transcript path"},
		{"missing file", Entry{ID: 5, Source: "claude", SessionID: "s", Metadata: meta(filepath.Join(t.TempDir(), "gone.jsonl"))}, "the file is missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := activityFor(tc.e)
			if !strings.Contains(a.Unavailable, tc.reason) || len(a.Records) != 0 {
				t.Fatalf("activity: %+v, want unavailable containing %q", a, tc.reason)
			}
		})
	}
}
