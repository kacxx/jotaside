package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func titledEntry(source, path string) Entry {
	meta, _ := json.Marshal(map[string]string{"transcript_path": path})
	return Entry{ID: 1, Source: source, Metadata: meta}
}

func titleOf(t *testing.T, source, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	es := []Entry{titledEntry(source, path)}
	(&Service{}).AddSessionTitles(es)
	return es[0].SessionTitle
}

const (
	titleUserLine = `{"type":"user","message":{"content":"hi"}}` + "\n"
	customOne     = `{"type":"custom-title","customTitle":"ABC-123 review","sessionId":"s"}` + "\n"
	customTwo     = `{"type":"custom-title","customTitle":"ABC-124 renamed","sessionId":"s"}` + "\n"
	aiTitleOne    = `{"type":"ai-title","aiTitle":"Generated name","sessionId":"s"}` + "\n"
)

func TestSessionTitleCustom(t *testing.T) {
	if got := titleOf(t, "claude", titleUserLine+customOne); got != "ABC-123 review" {
		t.Errorf("title = %q", got)
	}
}

func TestSessionTitleCustomBeatsNewerAITitle(t *testing.T) {
	if got := titleOf(t, "claude", customOne+aiTitleOne); got != "ABC-123 review" {
		t.Errorf("title = %q", got)
	}
}

func TestSessionTitleAIFallback(t *testing.T) {
	if got := titleOf(t, "claude", titleUserLine+aiTitleOne); got != "Generated name" {
		t.Errorf("title = %q", got)
	}
}

func TestSessionTitleRenamedNewestWins(t *testing.T) {
	if got := titleOf(t, "claude", customOne+titleUserLine+customTwo); got != "ABC-124 renamed" {
		t.Errorf("title = %q", got)
	}
}

func TestSessionTitleNone(t *testing.T) {
	if got := titleOf(t, "claude", titleUserLine); got != "" {
		t.Errorf("title = %q", got)
	}
}

func TestSessionTitleMissingTranscript(t *testing.T) {
	es := []Entry{titledEntry("claude", filepath.Join(t.TempDir(), "gone.jsonl"))}
	(&Service{}).AddSessionTitles(es)
	if es[0].SessionTitle != "" {
		t.Errorf("title = %q", es[0].SessionTitle)
	}
}

func TestSessionTitleOnlyForClaude(t *testing.T) {
	for _, source := range []string{"codex", "cursor", "cli", ""} {
		if got := titleOf(t, source, customOne); got != "" {
			t.Errorf("source %q: title = %q", source, got)
		}
	}
}

func TestSessionTitleReadsOnlyTheTail(t *testing.T) {
	// An old title beyond the tail window is not read; a cut first line is skipped.
	filler := `{"type":"user","message":{"content":"` + strings.Repeat("x", 1000) + `"}}` + "\n"
	content := customOne + strings.Repeat(filler, titleTailBytes/len(filler)+2)
	if got := titleOf(t, "claude", content); got != "" {
		t.Errorf("title = %q, want none from outside the tail", got)
	}
	if got := titleOf(t, "claude", content+customTwo); got != "ABC-124 renamed" {
		t.Errorf("title = %q", got)
	}
}

func TestSessionTitleRecordAtWindowStart(t *testing.T) {
	// The window begins exactly on a record boundary: that record is kept.
	const pre, post = `{"type":"user","message":{"content":"`, `"}}` + "\n"
	pad := titleTailBytes - len(customOne) - len(pre) - len(post)
	tail := customOne + pre + strings.Repeat("x", pad) + post
	if len(tail) != titleTailBytes {
		t.Fatalf("test setup: tail is %d bytes", len(tail))
	}
	if got := titleOf(t, "claude", titleUserLine+tail); got != "ABC-123 review" {
		t.Errorf("title = %q", got)
	}
}

func TestCleanTitleRemovesTerminalEscapes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Bad \x1b[31mred\x1b[0m tab\tnewline\nbell\a", "Bad red tab newline bell"},
		{"a\x1b]0;pwned\x07b", "ab"},
		{"a\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\b", "alinkb"},
		{"a\u009b31mb", "ab"},
		{"x\x1bMy", "xy"},
		{"  plain   title ", "plain title"},
		{"ABC-123 review", "ABC-123 review"},
	} {
		if got := cleanTitle(tc.in); got != tc.want {
			t.Errorf("cleanTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCleanTextKeepsLinesAndRemovesEscapes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		// A line erase, carriage return and cursor-up would let "hidden"
		// overwrite "visible" on a terminal; both stay visible.
		{"visible\x1b[2K\r\x1b[1Ahidden", "visible hidden"},
		{"see \x1b]8;;https://evil.example\x1b\\the docs\x1b]8;;\x1b\\ now", "see the docs now"},
		{"see \x1b]8;;https://evil.example\x07the docs\x1b]8;;\x07", "see the docs"},
		{"\x1b]0;pwned\x07title set", "title set"},
		{"bare\rreturn", "bare return"},
		{"a\u009b2Jb\u0085c", "ab c"},
		{"\x1b[31mred\x1b[0m\n\tindented\n\n  two  spaces ", "red\n\tindented\n\n  two  spaces "},
		// An unterminated OSC ends at the line, so the next line survives.
		{"one\x1b]0;unterminated\ntwo", "one\ntwo"},
		{"keep \u00e9 and \u2014 and emoji \U0001F600", "keep \u00e9 and \u2014 and emoji \U0001F600"},
	} {
		if got := cleanText(tc.in); got != tc.want {
			t.Errorf("cleanText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSessionTitleUnusableCustomFallsBackToAI(t *testing.T) {
	blank := `{"type":"custom-title","customTitle":"\u001b[0m \t"}` + "\n"
	if got := titleOf(t, "claude", blank+aiTitleOne); got != "Generated name" {
		t.Errorf("title = %q", got)
	}
}
