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
	"unicode/utf8"

	"github.com/kacxx/jotaside/internal/capture"
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
	// maxActivityReadBytes bounds how much of the start of a transcript is
	// read. Desktop transcripts reach tens of MB; past this the middle is left
	// unread and only the last maxActivityTailBytes are read as well.
	maxActivityReadBytes int64 = 32 << 20
	// maxActivityTailBytes is how much of the end of a transcript over the
	// read limit is read, so the session's last records are always seen.
	maxActivityTailBytes int64 = 8 << 20
	// maxActivityLineBytes is the longest transcript line that is decoded. A
	// longer one (a huge tool result) is skipped without being held in memory.
	maxActivityLineBytes = 4 << 20
)

// jotPlacementLines is how many lines from before the jot must start the
// part read after a skip for the jot to count as in that part. One is not
// enough: a line can carry an earlier time than the lines around it.
const jotPlacementLines = 10

// ActivityRecord is one thing that happened in a session after a jot.
type ActivityRecord struct {
	// Role is "user" for a prompt, "assistant" for a reply, or "tools" for the
	// tool calls between them, collapsed to one record.
	Role string    `json:"role"`
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Activity is what a jot's own session did after the jot was captured. jotaside
// makes no judgement about it; it only returns the evidence.
type Activity struct {
	JotID     int64     `json:"jot_id"`
	CreatedAt time.Time `json:"created_at"`
	Session   string    `json:"session_id,omitempty"`
	// Unavailable is why there is no transcript to read; "" when there is one.
	Unavailable string `json:"unavailable,omitempty"`
	// Records are the records after the jot, oldest first. Over the cap they
	// are the first and the last records, with the left-out part between.
	Records []ActivityRecord `json:"records,omitempty"`
	// Omitted is how many records the cap left out.
	Omitted int `json:"omitted,omitempty"`
	// OmittedAfter is how many of Records come before the left-out part. It is
	// set whenever Omitted or ReadLimited is.
	OmittedAfter *int `json:"omitted_after,omitempty"`
	// ReadLimited is set when the transcript was too large to read whole: its
	// start and its end were read, and the middle, where OmittedAfter points,
	// was not, so there may be records beyond those counted in Omitted.
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

// activityFor reads the transcript of e's session, if jotaside can.
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
		a.Records, a.Omitted, a.OmittedAfter, a.ReadLimited = nil, 0, nil, false
		a.Unavailable = "the transcript cannot be read: " + cleanTitle(err.Error())
	}
	return a
}

// readActivity fills a with the records of the Claude Code transcript at path
// that were written after t: the user's prompts, the assistant's text, and
// the tool calls between them collapsed to one record each time. Sidechains,
// meta records and compaction summaries are skipped, as in replyBefore.
//
// A transcript over maxActivityReadBytes is read in two parts: its first
// maxActivityReadBytes, then its last maxActivityTailBytes, so the end of the
// session is always seen. The two parts never overlap.
func readActivity(path string, t time.Time, a *Activity) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("the file is missing")
		}
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	c := &activityCollector{after: t}
	r := bufio.NewReaderSize(f, 64<<10)
	var read int64
	jumped := false
	for {
		line, n, err := readBoundedLine(r, maxActivityLineBytes)
		read += int64(n)
		if line != nil {
			c.add(line)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			break
		}
		if jumped || read < maxActivityReadBytes {
			continue
		}
		// The start has been read. Read on from the last part of the file, or
		// from here if the last part starts before here, so nothing is read
		// twice.
		jumped = true
		tail := info.Size() - maxActivityTailBytes
		if tail <= read {
			continue
		}
		// Seek one byte early and drop everything up to the first newline: if
		// tail is the start of a line, only the newline before it is dropped.
		if _, err := f.Seek(tail-1, io.SeekStart); err != nil {
			return err
		}
		r.Reset(f)
		c.skipMiddle()
		if _, _, err := readBoundedLine(r, 0); err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			break
		}
	}
	c.finish(a)
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

// activityCollector turns transcript lines into capped activity records: the
// first records up to half the cap (head), then the last records in the rest
// of the cap (tail), dropping the oldest of the tail as newer ones arrive.
type activityCollector struct {
	after time.Time // only records written after this are kept

	head      []ActivityRecord
	headRunes int
	headDone  bool // head is closed; later records go to the tail
	tail      []ActivityRecord
	tailN     []int // rune count of each tail record
	tailRunes int
	omitted   int

	// latest is the latest timestamp seen on any line. Timestamps can go
	// backwards (lines written late, history carried over from an earlier
	// session), so no single line says where the reading is; the latest does.
	latest time.Time

	// middlePending is set when the middle of the file was skipped before
	// anything after the jot was seen. The first timestamps read after the
	// skip tell whether the jot is in the part read (nothing was missed) or
	// in the skipped middle (the start of the activity is unread).
	middlePending bool
	pendingLines  int  // timestamped lines read since the skip
	middleUnread  bool // part of the activity after the jot was not read

	toolCalls map[string]int // pending tool group: calls by tool name
	toolCount int
	toolAt    time.Time
}

func (c *activityCollector) add(line []byte) {
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
	if json.Unmarshal(line, &rec) != nil {
		return
	}
	ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil {
		return
	}
	if ts.After(c.latest) {
		c.latest = ts
	}
	if c.middlePending {
		if ts.After(c.after) {
			// A line after the jot came before jotPlacementLines lines from
			// before it, so the jot may be in the skipped middle.
			c.startUnread()
		} else if c.pendingLines++; c.pendingLines >= jotPlacementLines {
			// Enough lines from before the jot: it is in the part read.
			c.middlePending = false
		}
	}
	if rec.IsSidechain || rec.IsMeta || rec.IsCompactSummary || !ts.After(c.after) {
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

// skipMiddle is called where reading jumps from the start of the file to its
// end. Records after the jot already seen stay as the head; those in the tail
// are left out, so the tail holds only records from the end of the file and
// runs unbroken to it.
func (c *activityCollector) skipMiddle() {
	c.flushTools()
	if len(c.head) == 0 && !c.latest.After(c.after) {
		// Nothing after the jot yet: the jot may be in the part still to be
		// read, in which case nothing after it was skipped.
		c.middlePending = true
		return
	}
	c.middleUnread, c.headDone = true, true
	c.omitted += len(c.tail)
	c.tail, c.tailN, c.tailRunes = nil, nil, 0
}

// startUnread settles a pending skip the cautious way: the jot is taken to
// be in the skipped middle, so the start of its activity is unread and what
// follows can only be the last records. Nothing has been kept yet: a pending
// skip is settled by the first line after the jot, before its record.
func (c *activityCollector) startUnread() {
	c.middlePending, c.middleUnread, c.headDone = false, true, true
}

// finish flushes pending tool calls and fills a's records and counts.
func (c *activityCollector) finish(a *Activity) {
	c.flushTools()
	if c.middlePending {
		// Too little was read after the skip to place the jot, so the
		// skipped middle may have held records after it.
		c.startUnread()
	}
	a.Records = append(append([]ActivityRecord(nil), c.head...), c.tail...)
	a.Omitted, a.ReadLimited = c.omitted, c.middleUnread
	if a.Omitted > 0 || a.ReadLimited {
		n := len(c.head)
		a.OmittedAfter = &n
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

// push adds rec to the head while the head's half of the cap lasts, and to
// the tail after, where it may push the oldest tail records out. The text is
// printed by jotaside activity and returned by the MCP tool, so its terminal
// escapes and control characters are removed first.
func (c *activityCollector) push(rec ActivityRecord) {
	rec.Text = strings.TrimSpace(cleanText(rec.Text))
	if rec.Text == "" {
		// Nothing printable was left, e.g. a prompt of only escapes.
		return
	}
	n := utf8.RuneCountInString(rec.Text)
	if n > maxActivityRecordRunes {
		rec.Text = string([]rune(rec.Text)[:maxActivityRecordRunes-1]) + "…"
		n = maxActivityRecordRunes
	}
	if !c.headDone {
		if c.headRunes+n <= maxActivityRunes/2 || len(c.head) == 0 {
			c.headRunes += n
			c.head = append(c.head, rec)
			return
		}
		c.headDone = true
	}
	c.pushTail(rec, n)
}

// pushTail adds rec, of n runes, to the tail. The tail has whatever part of
// the cap the head didn't use, so a session within the cap is kept whole. It
// always keeps its newest record.
func (c *activityCollector) pushTail(rec ActivityRecord, n int) {
	c.tail, c.tailN = append(c.tail, rec), append(c.tailN, n)
	c.tailRunes += n
	for c.headRunes+c.tailRunes > maxActivityRunes && len(c.tail) > 1 {
		c.tailRunes -= c.tailN[0]
		c.tail, c.tailN = c.tail[1:], c.tailN[1:]
		c.omitted++
	}
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
