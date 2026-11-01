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
	if a.Unavailable != "" || a.Omitted != 0 || a.OmittedAfter != nil || a.ReadLimited {
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
		j(userLine("2026-10-12T02:17:04Z", "\x1b[2J\r")),
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

// at returns the RFC 3339 time i seconds after the jot (before it, for i < 0).
func at(i int) string {
	return activityJotAt.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
}

// texts returns the records' text, cut to the part before any padding.
func texts(a Activity) []string {
	var got []string
	for _, r := range a.Records {
		got = append(got, strings.TrimRight(strings.SplitN(r.Text, " ", 2)[0], "…"))
	}
	return got
}

func noDuplicates(t *testing.T, a Activity) {
	t.Helper()
	seen := map[string]bool{}
	for i, r := range a.Records {
		if seen[r.Text] {
			t.Fatalf("record %q appears twice", r.Text)
		}
		seen[r.Text] = true
		if i > 0 && r.At.Before(a.Records[i-1].At) {
			t.Fatalf("records out of order at %d: %v", i, texts(a))
		}
		if !r.At.After(activityJotAt) {
			t.Fatalf("record %q is from before the jot", r.Text)
		}
	}
}

func TestActivityWithinTheCapIsWhole(t *testing.T) {
	// Seven records of 1,500 characters: more than the head's half of the cap,
	// but within the cap, so all are returned, oldest first.
	var lines []string
	for i := range 7 {
		lines = append(lines, assistantLine(at(1+i), fmt.Sprintf("r%d %s", i, strings.Repeat("x", 1490))))
	}
	a := activityFor(writeTranscript(t, lines...))
	if got := strings.Join(texts(a), ","); got != "r0,r1,r2,r3,r4,r5,r6" {
		t.Fatalf("records = %s", got)
	}
	if a.Omitted != 0 || a.OmittedAfter != nil || a.ReadLimited {
		t.Fatalf("nothing is left out: %+v", a)
	}
}

func TestActivityOverTheCapKeepsTheFirstAndTheLast(t *testing.T) {
	var lines []string
	for i := range 20 {
		lines = append(lines, assistantLine(at(1+i), fmt.Sprintf("r%d %s", i, strings.Repeat("x", 5000))))
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
	if total > maxActivityRunes {
		t.Fatalf("kept %d runes, over the cap", total)
	}
	if !strings.HasSuffix(a.Records[0].Text, "…") {
		t.Fatalf("an over-long record is cut with an ellipsis: %q", a.Records[0].Text[len(a.Records[0].Text)-5:])
	}
	// Each record is cut to 1,500: four fit in each half of the cap.
	if got := strings.Join(texts(a), ","); got != "r0,r1,r2,r3,r16,r17,r18,r19" {
		t.Fatalf("records = %s", got)
	}
	if a.Omitted != 12 || a.OmittedAfter == nil || *a.OmittedAfter != 4 || a.ReadLimited {
		t.Fatalf("omitted %d after %v, read limited %v", a.Omitted, a.OmittedAfter, a.ReadLimited)
	}
	noDuplicates(t, a)
}

// shrinkReads makes the read limits small enough to test with short files.
func shrinkReads(t *testing.T, head, tail int64, line int) {
	t.Helper()
	read, tl, ln := maxActivityReadBytes, maxActivityTailBytes, maxActivityLineBytes
	t.Cleanup(func() { maxActivityReadBytes, maxActivityTailBytes, maxActivityLineBytes = read, tl, ln })
	maxActivityReadBytes, maxActivityTailBytes, maxActivityLineBytes = head, tail, line
}

// numbered returns n assistant replies "reply <first+i>", one second apart
// from the jot time plus first+1.
func numbered(first, n int) []string {
	var lines []string
	for i := first; i < first+n; i++ {
		lines = append(lines, assistantLine(at(1+i), fmt.Sprintf("reply %d", i)))
	}
	return lines
}

func TestActivityOverTheReadLimitReadsTheEnd(t *testing.T) {
	shrinkReads(t, 3000, 3000, 1000)
	big := assistantLine(at(0), strings.Repeat("y", 1400)) // longer than a line may be
	lines := append([]string{big}, numbered(0, 200)...)    // about 20 KB
	a := activityFor(writeTranscript(t, lines...))
	got := texts(a)
	if !a.ReadLimited || a.OmittedAfter == nil {
		t.Fatalf("the middle was not read, so the result should say so: %+v", a)
	}
	first, last := a.Records[0].Text, a.Records[len(a.Records)-1].Text
	if first != "reply 0" || last != "reply 199" {
		t.Fatalf("first %q, last %q; the oversized line is skipped and the file's last record is read", first, last)
	}
	k := *a.OmittedAfter
	if k == 0 || k >= len(a.Records) || a.Records[k].Text == fmt.Sprintf("reply %d", k) {
		t.Fatalf("the gap after %d records should fall between the start and the end: %v", k, got)
	}
	if len(a.Records) >= 100 {
		t.Fatalf("only the start and the end are read, got %d records", len(a.Records))
	}
	noDuplicates(t, a)
}

func TestActivityRecordsBeforeTheSkipAreLeftOut(t *testing.T) {
	// The head fills within the start read and a few records wait in the
	// tail when reading jumps. Those are left out, so the tail holds only
	// records from the end of the file and there is one gap, not two.
	shrinkReads(t, 12000, 2000, 4000)
	var lines []string
	for i := range 30 {
		lines = append(lines, assistantLine(at(1+i), fmt.Sprintf("r%d %s", i, strings.Repeat("x", 1400))))
	}
	a := activityFor(writeTranscript(t, lines...))
	if got := strings.Join(texts(a), ","); got != "r0,r1,r2,r3,r29" {
		t.Fatalf("records = %s", got)
	}
	if !a.ReadLimited || a.OmittedAfter == nil || *a.OmittedAfter != 4 || a.Omitted < 4 {
		t.Fatalf("omitted %d after %v, read limited %v", a.Omitted, a.OmittedAfter, a.ReadLimited)
	}
}

func TestActivityTailReadStartsAtALine(t *testing.T) {
	// The tail starts exactly at the start of a line: that line is read, not
	// dropped as a partial one.
	lines := numbered(0, 100)
	last := strings.Join(lines[90:], "\n") + "\n"
	shrinkReads(t, 1000, int64(len(last)), 1000)
	a := activityFor(writeTranscript(t, lines...))
	k := *a.OmittedAfter
	if a.Records[k].Text != "reply 90" || a.Records[len(a.Records)-1].Text != "reply 99" {
		t.Fatalf("tail records: %v", texts(a)[k:])
	}
	noDuplicates(t, a)
}

func TestActivityJustOverTheReadLimitReadsNothingTwice(t *testing.T) {
	// 40 replies of about 115 bytes: over the 3,000-byte start, but the last
	// 3,000 bytes begin before the start ends, so reading just carries on.
	shrinkReads(t, 3000, 3000, 1000)
	a := activityFor(writeTranscript(t, numbered(0, 40)...))
	if a.ReadLimited || a.Omitted != 0 || a.OmittedAfter != nil || len(a.Records) != 40 {
		t.Fatalf("the whole file is read once: %d records, %+v", len(a.Records), a)
	}
	noDuplicates(t, a)
}

func TestActivityJotPastTheStartRead(t *testing.T) {
	shrinkReads(t, 3000, 3000, 1000)
	before := func(n int) []string {
		var lines []string
		for i := n; i > 0; i-- {
			lines = append(lines, userLine(at(-i), fmt.Sprintf("before %d", i)))
		}
		return lines
	}

	t.Run("in the end read", func(t *testing.T) {
		// The jot is in the last part read, so nothing after it was skipped.
		lines := append(before(200), numbered(0, 10)...)
		a := activityFor(writeTranscript(t, lines...))
		if len(a.Records) != 10 {
			t.Fatalf("records = %v", texts(a))
		}
		for i, r := range a.Records {
			if r.Text != fmt.Sprintf("reply %d", i) {
				t.Fatalf("record %d = %q", i, r.Text)
			}
		}
		if a.ReadLimited || a.Omitted != 0 || a.OmittedAfter != nil {
			t.Fatalf("nothing after the jot was skipped: %+v", a)
		}
		noDuplicates(t, a)
	})

	t.Run("in the skipped middle", func(t *testing.T) {
		// The jot falls in the part that is not read: only the last records
		// are returned, with the gap before them, and none from before the jot.
		lines := append(before(100), numbered(0, 100)...)
		a := activityFor(writeTranscript(t, lines...))
		if !a.ReadLimited || a.OmittedAfter == nil || *a.OmittedAfter != 0 {
			t.Fatalf("the start of the activity is unread and should be marked: %+v", a)
		}
		if len(a.Records) == 0 || a.Records[len(a.Records)-1].Text != "reply 99" {
			t.Fatalf("records = %v", texts(a))
		}
		noDuplicates(t, a)
	})
}

func TestActivityTailIsBounded(t *testing.T) {
	// A long session read whole: the kept records stay within the cap however
	// many there are.
	a := activityFor(writeTranscript(t, numbered(0, 20000)...))
	total := 0
	for _, r := range a.Records {
		total += len([]rune(r.Text))
	}
	if total > maxActivityRunes || a.Omitted+len(a.Records) != 20000 || a.Records[len(a.Records)-1].Text != "reply 19999" {
		t.Fatalf("%d records (%d runes), %d omitted", len(a.Records), total, a.Omitted)
	}
	noDuplicates(t, a)
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
		{"jotaside add", Entry{ID: 1, Source: "cli"}, "not captured in an agent session"},
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
