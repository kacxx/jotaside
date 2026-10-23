package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kacxx/aside-jot/internal/capture"
)

const (
	// maxActivityRunes caps the text of all the records Activity returns.
	maxActivityRunes = 12000
	// maxActivityRecordRunes caps one record, so a single long reply can't
	// use the whole budget.
	maxActivityRecordRunes = 1500
)

// Variables, not constants, so tests can shrink them.
var (
	// maxActivityReadBytes bounds how much of a transcript is read. Desktop
	// transcripts reach several MB; past this the rest is left unread.
	maxActivityReadBytes int64 = 32 << 20
	// maxActivityLineBytes is the longest transcript line that is decoded. A
	// longer one (a huge tool result) is skipped without being held in memory.
	maxActivityLineBytes = 4 << 20
)

// ActivityRecord is one thing that happened in a session after a jot.
type ActivityRecord struct {
	// Role is "user" for a prompt, "assistant" for a reply, or "tools" for the
	// tool calls between them, collapsed to one record.
	Role string    `json:"role"`
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Activity is what a jot's own session did after the jot was captured. aside
// makes no judgement about it; it only returns the evidence.
type Activity struct {
	JotID     int64     `json:"jot_id"`
	CreatedAt time.Time `json:"created_at"`
	Session   string    `json:"session_id,omitempty"`
	// Unavailable is why there is no transcript to read; "" when there is one.
	Unavailable string `json:"unavailable,omitempty"`
	// Records are the records after the jot, oldest first, within the cap.
	Records []ActivityRecord `json:"records,omitempty"`
	// Omitted is how many further records the cap left out.
	Omitted int `json:"omitted,omitempty"`
	// ReadLimited is set when reading stopped at the size limit, so there may
	// be records beyond those counted in Omitted.
	ReadLimited bool `json:"read_limited,omitempty"`
}

// Activity returns what the session of jot id did after the jot.
func (s *Service) Activity(ctx context.Context, id int64) (Activity, error) {
	e, err := s.store.Get(ctx, id)
	if err != nil {
		return Activity{}, err
	}
	return activityFor(e), nil
}

// activityFor reads the transcript of e's session, if aside can.
func activityFor(e Entry) Activity {
	a := Activity{JotID: e.ID, CreatedAt: e.CreatedAt, Session: e.SessionID}
	switch e.Source {
	case "claude":
	case "codex":
		a.Unavailable = "Codex transcripts are not read yet"
		return a
	case "cursor":
		a.Unavailable = "Cursor jots have no transcript"
		return a
	default:
		a.Unavailable = "this jot was not captured in an agent session, so it has no transcript"
		return a
	}
	var meta struct {
		TranscriptPath string `json:"transcript_path"`
	}
	if json.Unmarshal(e.Metadata, &meta) != nil || meta.TranscriptPath == "" {
		a.Unavailable = "this jot did not record a transcript path"
		return a
	}
	if err := readActivity(meta.TranscriptPath, e.CreatedAt, &a); err != nil {
		a.Records, a.Omitted, a.ReadLimited = nil, 0, false
		a.Unavailable = "the transcript cannot be read: " + cleanTitle(err.Error())
	}
	return a
}

// readActivity fills a with the records of the Claude Code transcript at path
// that were written after t: the user's prompts, the assistant's text, and
// the tool calls between them collapsed to one record each time. Sidechains,
// meta records and compaction summaries are skipped, as in replyBefore.
func readActivity(path string, t time.Time, a *Activity) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("the file is missing")
		}
		return err
	}
	defer f.Close()
	c := &activityCollector{}
	r := bufio.NewReaderSize(f, 64<<10)
	var read int64
	for {
		line, n, err := readBoundedLine(r, maxActivityLineBytes)
		read += int64(n)
		if line != nil {
			c.add(line, t)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			break
		}
		if read >= maxActivityReadBytes {
			a.ReadLimited = true
			break
		}
	}
	c.flushTools()
	a.Records, a.Omitted = c.records, c.omitted
	return nil
}

// readBoundedLine reads one line. It returns the line's full length n, and
// the line itself unless it is longer than max, in which case the line is read
// through and dropped.
func readBoundedLine(r *bufio.Reader, max int) (line []byte, n int, err error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		n += len(chunk)
		if n <= max {
			buf = append(buf, chunk...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if n > max {
			return nil, n, err
		}
		return buf, n, err
	}
}

// activityCollector turns transcript lines into capped activity records.
type activityCollector struct {
	records []ActivityRecord
	runes   int
	omitted int
	full    bool

	toolCalls map[string]int // pending tool group: calls by tool name
	toolCount int
	toolAt    time.Time
}

func (c *activityCollector) add(line []byte, after time.Time) {
	var rec struct {
		Type             string `json:"type"`
		Timestamp        string `json:"timestamp"`
		IsSidechain      bool   `json:"isSidechain"`
		IsMeta           bool   `json:"isMeta"`
		IsCompactSummary bool   `json:"isCompactSummary"`
		Message          struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.IsSidechain || rec.IsMeta || rec.IsCompactSummary {
		return
	}
	ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil || !ts.After(after) {
		return
	}
	switch rec.Type {
	case "assistant":
		names := toolUseNames(rec.Message.Content)
		for _, name := range names {
			c.addTool(name, ts)
		}
		if text := strings.TrimSpace(cleanText(contentText(rec.Message.Content, "text"))); text != "" {
			c.flushTools()
			c.push(ActivityRecord{Role: "assistant", At: ts, Text: text})
		}
	case "user":
		if userRecordKind(rec.Message.Content) != userPrompt {
			return
		}
		text := strings.TrimSpace(capture.StripSystemReminders(contentText(rec.Message.Content, "text")))
		if text == "" {
			text = "[image]"
		}
		c.flushTools()
		c.push(ActivityRecord{Role: "user", At: ts, Text: text})
	}
}

func (c *activityCollector) addTool(name string, at time.Time) {
	if c.toolCalls == nil {
		c.toolCalls, c.toolAt = map[string]int{}, at
	}
	c.toolCalls[name]++
	c.toolCount++
}

// flushTools turns the pending tool calls into one record.
func (c *activityCollector) flushTools() {
	if c.toolCount == 0 {
		return
	}
	names := make([]string, 0, len(c.toolCalls))
	for n := range c.toolCalls {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if c.toolCalls[names[i]] != c.toolCalls[names[j]] {
			return c.toolCalls[names[i]] > c.toolCalls[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = n
		if k := c.toolCalls[n]; k > 1 {
			parts[i] = fmt.Sprintf("%s ×%d", n, k)
		}
	}
	noun := "tool calls"
	if c.toolCount == 1 {
		noun = "tool call"
	}
	rec := ActivityRecord{Role: "tools", At: c.toolAt, Text: fmt.Sprintf("%d %s: %s", c.toolCount, noun, strings.Join(parts, ", "))}
	c.toolCalls, c.toolCount = nil, 0
	c.push(rec)
}

// push keeps rec while the text budget lasts, and counts it as left out after.
// The text is printed by aside activity and returned by the MCP tool, so its
// terminal escapes and control characters are removed first.
func (c *activityCollector) push(rec ActivityRecord) {
	if c.full {
		c.omitted++
		return
	}
	rec.Text = strings.TrimSpace(cleanText(rec.Text))
	if rs := []rune(rec.Text); len(rs) > maxActivityRecordRunes {
		rec.Text = string(rs[:maxActivityRecordRunes-1]) + "…"
	}
	n := len([]rune(rec.Text))
	if c.runes+n > maxActivityRunes && len(c.records) > 0 {
		c.full = true
		c.omitted++
		return
	}
	c.runes += n
	c.records = append(c.records, rec)
}

// toolUseNames returns the names of the tool_use blocks in a message's content.
func toolUseNames(raw json.RawMessage) []string {
	var blocks []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var names []string
	for _, b := range blocks {
		if b.Type == "tool_use" {
			name := b.Name
			if name == "" {
				name = "tool"
			}
			names = append(names, name)
		}
	}
	return names
}
