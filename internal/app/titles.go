package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode"
)

// titleTailBytes is how much of the end of a transcript is searched for the
// session's title. Claude Code appends title records throughout a session, so
// the newest is near the end; the whole file is never read.
const titleTailBytes = 256 << 10

// AddSessionTitles fills in SessionTitle for the Claude Code jots in es whose
// transcript has a title. It is read from the transcript now, not stored,
// because the title may not exist when the jot is captured and can be renamed.
// A jot with no title, no readable transcript or another source is left alone.
func (s *Service) AddSessionTitles(es []Entry) {
	byPath := map[string]string{}
	for i, e := range es {
		if e.Source != "claude" {
			continue
		}
		var meta struct {
			TranscriptPath string `json:"transcript_path"`
		}
		if json.Unmarshal(e.Metadata, &meta) != nil || meta.TranscriptPath == "" {
			continue
		}
		title, ok := byPath[meta.TranscriptPath]
		if !ok {
			title = transcriptTitle(meta.TranscriptPath)
			byPath[meta.TranscriptPath] = title
		}
		es[i].SessionTitle = title
	}
}

// transcriptTitle returns the newest custom-title in the tail of the Claude
// Code transcript at path, else the newest ai-title, else "".
func transcriptTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	// Read one byte before the window, to tell whether it starts on a record.
	start := info.Size() - titleTailBytes - 1
	if start < 0 {
		start = 0
	}
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return ""
	}
	lines := bytes.Split(buf, []byte("\n"))
	if start > 0 {
		lines = lines[1:] // the first line may be cut off; the byte before the window is its newline if not
	}
	var custom, ai string
	for _, line := range lines {
		if !bytes.Contains(line, []byte("-title\"")) {
			continue
		}
		var rec struct {
			Type        string `json:"type"`
			CustomTitle string `json:"customTitle"`
			AITitle     string `json:"aiTitle"`
		}
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		switch {
		case rec.Type == "custom-title" && rec.CustomTitle != "":
			custom = rec.CustomTitle
		case rec.Type == "ai-title" && rec.AITitle != "":
			ai = rec.AITitle
		}
	}
	if title := cleanTitle(custom); title != "" {
		return title
	}
	return cleanTitle(ai)
}

// terminalEscapes matches the escape sequences a terminal acts on: CSI (ESC [
// or the 8-bit form, parameters, a final byte), OSC (ESC ] up to BEL or ST,
// such as an OSC 8 hyperlink or an OSC 0 window title) and other two-byte ESC
// sequences. An OSC's text can't hold control characters, so one left
// unterminated ends at the next line instead of taking the rest of the text.
var terminalEscapes = regexp.MustCompile(`(?:\x1b\[|\x{9b})[0-?]*[ -/]*[@-~]|\x1b\][^\x00-\x1f\x7f-\x{9f}]*(?:\x07|\x1b\\)?|\x1b[@-Z\\-_]`)

// cleanTitle makes a transcript's title safe to print: terminal escape
// sequences are removed whole, any other control character becomes a space,
// and runs of whitespace collapse.
func cleanTitle(s string) string {
	s = terminalEscapes.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
}

// cleanText makes multi-line text from a transcript or a hook payload safe to
// print or publish: terminal escape sequences are removed whole, newlines and
// tabs are kept, and any other control character, such as a carriage return
// that would let later text overwrite a line, becomes a space. Other
// whitespace is left as it is.
func cleanText(s string) string {
	s = terminalEscapes.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
