package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Find also lists the Claude Code sessions whose transcripts acted on the
// query (#35): it was in a prompt the person typed (slash-command arguments,
// ! commands and prompts queued while the agent was busy included) or in the
// input of one of the session's tool calls. Tool output, loaded context
// (memory and instruction files, which arrive inside <system-reminder> blocks
// or as attachments) and the model's own text are never read, and
// scheduled-task runs are left out. A subagent's transcript counts for the
// session that started it.
//
// Every transcript is read on each find, so the scan first looks for the
// queries as plain bytes, any ASCII case, and parses only the files and lines
// that have one. FindEach reads the transcripts once for many queries.

// SetTranscriptsDir sets Claude Code's projects directory, which holds
// <project>/<session id>.jsonl and <project>/<session id>/subagents/*.jsonl.
// "" (the default) turns the scan off.
func (s *Service) SetTranscriptsDir(dir string) { s.transcripts = dir }

// ClaudeProjectsDir is where Claude Code keeps transcripts:
// $CLAUDE_CONFIG_DIR/projects, else ~/.claude/projects; "" if neither is known.
func ClaudeProjectsDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "projects")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// prefilterChunk is how much of a transcript the prefilter reads at a time.
const prefilterChunk = 1 << 20

var (
	reminderBlock = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)
	// A user record the harness wrote rather than the person: it opens with
	// a hyphenated lowercase tag (<command-name>, <local-command-stdout>,
	// <task-notification>). A typed prompt that opens with pasted XML or HTML
	// (<div>, <project>) doesn't.
	harnessWrapped = regexp.MustCompile(`^<[a-z]+(?:-[a-z]+)+[\s>]`)
	// The parts of a wrapped record the person typed: a slash command's
	// arguments and a ! command.
	typedParts = regexp.MustCompile(`(?s)<(command-args|bash-input)>(.*?)</(?:command-args|bash-input)>`)
)

// continuedMarker starts the summary Claude Code writes as a user record when
// a session runs out of context; it quotes the earlier conversation.
const continuedMarker = "This session is being continued"

// scannedSession is a Claude Code session the scan found.
type scannedSession struct {
	id         string
	path       string // its transcript
	lastActive time.Time
}

// transcriptFile is one transcript and the session it counts for.
type transcriptFile struct {
	path    string
	session int // index into the sessions listTranscripts returns
	sub     bool
	mod     time.Time
}

// scanTranscripts returns, for each matcher, the sessions in dir that acted
// on its query.
func scanTranscripts(ctx context.Context, dir string, ms []queryMatcher) ([][]scannedSession, error) {
	out := make([][]scannedSession, len(ms))
	if dir == "" || len(ms) == 0 {
		return out, nil
	}
	sessions, files := listTranscripts(dir)
	if len(files) == 0 {
		return out, ctx.Err()
	}
	needles := make([][]byte, len(ms))
	for i, m := range ms {
		needles[i] = m.needle
	}
	hits := make([][]bool, len(files))
	if err := forEach(ctx, len(files), func(buf *[]byte, i int) {
		hits[i] = fileHits(ctx, files[i].path, needles, buf)
	}); err != nil {
		return nil, err
	}

	// Each session's files, and the queries the prefilter found in any.
	byIndex := make([][]int, len(sessions))
	for i, f := range files {
		byIndex[f.session] = append(byIndex[f.session], i)
	}
	var candidates []int
	for si, fs := range byIndex {
		for _, i := range fs {
			if hits[i] != nil {
				candidates = append(candidates, si)
				break
			}
		}
	}
	acted := make([][]bool, len(candidates))
	if err := forEach(ctx, len(candidates), func(_ *[]byte, c int) {
		acted[c] = sessionActed(ctx, files, byIndex[candidates[c]], hits, ms)
	}); err != nil {
		return nil, err
	}
	for c, si := range candidates {
		s := sessions[si]
		for _, i := range byIndex[si] {
			if files[i].mod.After(s.lastActive) {
				s.lastActive = files[i].mod
			}
		}
		for q, ok := range acted[c] {
			if ok {
				out[q] = append(out[q], s)
			}
		}
	}
	return out, ctx.Err()
}

// sessionActed reports, for each matcher, whether a session acted on its
// query. fs are the session's files (its transcript first), hits what the
// prefilter found in each. A scheduled-task run acts on nothing.
func sessionActed(ctx context.Context, files []transcriptFile, fs []int, hits [][]bool, ms []queryMatcher) []bool {
	acted := make([]bool, len(ms))
	for n, i := range fs {
		var want []int // queries in this file that aren't settled yet
		for q, hit := range hits[i] {
			if hit && !acted[q] {
				want = append(want, q)
			}
		}
		first := n == 0 && !files[i].sub
		if len(want) == 0 && !first {
			continue
		}
		scheduled := readTranscript(ctx, files[i].path, ms, want, first, acted)
		if scheduled {
			return make([]bool, len(ms))
		}
	}
	return acted
}

// listTranscripts lists dir's sessions, from their transcripts, and their
// files: each transcript, then its subagents' transcripts. Subagent files
// whose session's transcript is gone are left out: the session can't be
// resumed.
func listTranscripts(dir string) ([]scannedSession, []transcriptFile) {
	projects, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var sessions []scannedSession
	var files []transcriptFile
	add := func(path string, session int, sub bool) bool {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
		files = append(files, transcriptFile{path: path, session: session, sub: sub, mod: info.ModTime()})
		return true
	}
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		pdir := filepath.Join(dir, p.Name())
		entries, err := os.ReadDir(pdir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			path := filepath.Join(pdir, name)
			if !add(path, len(sessions), false) {
				continue
			}
			id := strings.TrimSuffix(name, ".jsonl")
			subs, _ := filepath.Glob(filepath.Join(pdir, id, "subagents", "*.jsonl"))
			for _, sp := range subs {
				add(sp, len(sessions), true)
			}
			sessions = append(sessions, scannedSession{id: id, path: path})
		}
	}
	return sessions, files
}

// forEach runs f(i) for i in [0, n) on a few goroutines, each with its own
// buffer to reuse, and returns ctx's error if it was cancelled.
func forEach(ctx context.Context, n int, f func(buf *[]byte, i int)) error {
	workers := min(n, max(1, runtime.GOMAXPROCS(0)))
	next := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var buf []byte
			for i := range next {
				f(&buf, i)
			}
		}()
	}
	var err error
feed:
	for i := range n {
		select {
		case next <- i:
		case <-ctx.Done():
			err = ctx.Err()
			break feed
		}
	}
	close(next)
	wg.Wait()
	if err == nil {
		err = ctx.Err()
	}
	return err
}

// fileHits reports which needles (lowercase ASCII) the file has, in any ASCII
// case, or nil if it has none or can't be read. An empty needle is in every
// file. *buf is reused between calls.
func fileHits(ctx context.Context, path string, needles [][]byte, buf *[]byte) []bool {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	found := make([]bool, len(needles))
	left, longest := 0, 0
	for i, n := range needles {
		if len(n) == 0 {
			found[i] = true
			continue
		}
		left++
		longest = max(longest, len(n))
	}
	hit := left < len(needles)
	groups := groupNeedles(needles, nil)
	if left > 0 {
		if need := prefilterChunk + longest; len(*buf) < need {
			*buf = make([]byte, need)
		}
		b := *buf
		keep := 0 // bytes carried over from the last read, so a match can straddle reads
		for left > 0 && ctx.Err() == nil {
			n, err := io.ReadFull(f, b[keep:keep+prefilterChunk])
			lowerASCII(b[keep : keep+n])
			window := b[:keep+n]
			if k := markNeedles(window, needles, groups, found); k > 0 {
				hit = true
				left -= k
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
					return nil
				}
				break
			}
			keep = min(longest-1, len(window))
			copy(b, window[len(window)-keep:])
		}
	}
	if !hit {
		return nil
	}
	return found
}

// needleGroup is needles that start with the same bytes, anchor: one search
// for the anchor finds where any of them can be. Keys from one tracker
// (ABC-1, ABC-22) share "abc-", so many keys cost about one pass.
type needleGroup struct {
	anchor  []byte
	members []int // indexes into the needles
}

// groupNeedles groups the non-empty needles (those at idx, or all if idx is
// nil) by their first few bytes; each group's anchor is its members' longest
// common prefix.
func groupNeedles(needles [][]byte, idx []int) []*needleGroup {
	if idx == nil {
		idx = make([]int, len(needles))
		for i := range idx {
			idx[i] = i
		}
	}
	const head = 4
	byHead := map[string]*needleGroup{}
	var groups []*needleGroup
	for _, i := range idx {
		n := needles[i]
		if len(n) == 0 {
			continue
		}
		k := string(n[:min(head, len(n))])
		g, ok := byHead[k]
		if !ok {
			g = &needleGroup{anchor: n}
			byHead[k] = g
			groups = append(groups, g)
		}
		l := 0
		for l < len(g.anchor) && l < len(n) && g.anchor[l] == n[l] {
			l++
		}
		g.anchor = g.anchor[:l]
		g.members = append(g.members, i)
	}
	return groups
}

// markNeedles sets found[i] for each grouped needle window has, skipping
// those already set, and returns how many it set.
func markNeedles(window []byte, needles [][]byte, groups []*needleGroup, found []bool) int {
	set := 0
	for _, g := range groups {
		for off := 0; ; {
			pending := false
			for _, i := range g.members {
				if !found[i] {
					pending = true
					break
				}
			}
			if !pending {
				break
			}
			at := bytes.Index(window[off:], g.anchor)
			if at < 0 {
				break
			}
			at += off
			for _, i := range g.members {
				if !found[i] && bytes.HasPrefix(window[at:], needles[i]) {
					found[i] = true
					set++
				}
			}
			off = at + 1
		}
	}
	return set
}

// lowerASCII lowercases the ASCII letters in b in place.
func lowerASCII(b []byte) {
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
}

// containsFold reports whether s contains needle (lowercase) in any ASCII
// case, as SQLite's LIKE does: other letters must match exactly.
func containsFold(s, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(s); i++ {
		j := 0
		for ; j < len(needle); j++ {
			c := s[i+j]
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != needle[j] {
				break
			}
		}
		if j == len(needle) {
			return true
		}
	}
	return false
}

// record is the part of a transcript line the scan reads.
type record struct {
	Type       string          `json:"type"`
	IsMeta     bool            `json:"isMeta"`
	Message    json.RawMessage `json:"message"`
	Attachment struct {
		Type        string          `json:"type"`
		CommandMode string          `json:"commandMode"`
		Prompt      json.RawMessage `json:"prompt"`
	} `json:"attachment"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Input json.RawMessage `json:"input"`
}

// readTranscript reads the transcript's lines that have one of the queries in
// want, and sets acted[q] for each query a line acted on. If first is set, it
// also reads the first typed prompt, to tell a scheduled-task run, and
// reports one as soon as it sees it. It stops once nothing is left to learn.
func readTranscript(ctx context.Context, path string, ms []queryMatcher, want []int, first bool, acted []bool) (scheduled bool) {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	var lower []byte
	var lineHas []int
	want = append([]int(nil), want...)
	needles := make([][]byte, len(ms))
	for i, m := range ms {
		needles[i] = m.needle
	}
	groups := groupNeedles(needles, want)
	lineFound := make([]bool, len(ms))
	for n := 0; ; n++ {
		if n%1024 == 0 && ctx.Err() != nil {
			return false
		}
		if len(want) == 0 && !first {
			return false
		}
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			lineHas = lineHas[:0]
			if len(want) > 0 {
				lower = append(lower[:0], line...)
				lowerASCII(lower)
				for _, q := range want {
					lineFound[q] = acted[q] || len(needles[q]) == 0
				}
				markNeedles(lower, needles, groups, lineFound)
				for _, q := range want {
					if !acted[q] && (lineFound[q] || len(needles[q]) == 0) {
						lineHas = append(lineHas, q)
					}
				}
			}
			if len(lineHas) > 0 || first {
				var rec record
				if json.Unmarshal(line, &rec) == nil {
					if first && rec.Type == "user" && !rec.IsMeta {
						if typed := strings.TrimSpace(strings.Join(userTexts(rec.Message), " ")); typed != "" {
							first = false
							if strings.HasPrefix(typed, "<scheduled-task") {
								return true
							}
						}
					}
					if len(lineHas) > 0 {
						texts := actedTexts(rec)
						for _, q := range lineHas {
							for _, t := range texts {
								if ms[q].match(t) {
									acted[q] = true
									break
								}
							}
						}
						left := want[:0]
						for _, q := range want {
							if !acted[q] {
								left = append(left, q)
							}
						}
						want = left
					}
				}
			}
		}
		if err != nil {
			return false
		}
	}
}

// actedTexts returns the texts in a transcript record that count as acting:
// what the person typed, and the strings in its tool calls' inputs.
func actedTexts(rec record) []string {
	var out []string
	switch {
	case rec.Type == "user" && !rec.IsMeta:
		if t := typedText(strings.Join(userTexts(rec.Message), " ")); t != "" {
			out = append(out, t)
		}
	case rec.Type == "attachment" && rec.Attachment.Type == "queued_command" && rec.Attachment.CommandMode == "prompt":
		// A prompt typed while the agent was busy, added to its turn.
		var p string
		if json.Unmarshal(rec.Attachment.Prompt, &p) == nil {
			p = reminderBlock.ReplaceAllString(p, "")
		} else {
			p = strings.Join(textBlocks(rec.Attachment.Prompt), " ")
		}
		if t := typedText(p); t != "" {
			out = append(out, t)
		}
	}
	for _, b := range contentBlocks(rec.Message) {
		if b.Type == "tool_use" {
			out = appendStrings(out, b.Input)
		}
	}
	return out
}

// typedText is the part of a user prompt the person typed: all of it, or for
// a record the harness wrote, its slash-command arguments and ! command. A
// summary of an earlier conversation counts as nothing.
func typedText(text string) string {
	text = strings.TrimSpace(text)
	if harnessWrapped.MatchString(text) {
		var parts []string
		for _, m := range typedParts.FindAllStringSubmatch(text, -1) {
			parts = append(parts, m[2])
		}
		text = strings.Join(parts, " ")
	}
	if strings.Contains(text, continuedMarker) {
		return ""
	}
	return text
}

// userTexts returns a user message's text, without <system-reminder> blocks:
// the content if it is a string, else its text blocks. Tool results are not
// text blocks.
func userTexts(msg json.RawMessage) []string {
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(msg, &m) != nil {
		return nil
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return []string{reminderBlock.ReplaceAllString(s, "")}
	}
	return textBlocks(m.Content)
}

// textBlocks returns the text of the text blocks in content, without
// <system-reminder> blocks.
func textBlocks(content json.RawMessage) []string {
	var blocks []contentBlock
	if json.Unmarshal(content, &blocks) != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if b.Type == "text" {
			out = append(out, reminderBlock.ReplaceAllString(b.Text, ""))
		}
	}
	return out
}

// contentBlocks returns a message's content blocks, or nil if its content is
// a string.
func contentBlocks(msg json.RawMessage) []contentBlock {
	var m struct {
		Content []json.RawMessage `json:"content"`
	}
	if json.Unmarshal(msg, &m) != nil {
		return nil
	}
	out := make([]contentBlock, 0, len(m.Content))
	for _, raw := range m.Content {
		var b contentBlock
		if json.Unmarshal(raw, &b) == nil {
			out = append(out, b)
		}
	}
	return out
}

// appendStrings appends every string in a tool call's input, keys and values,
// decoded, so "\nABC-1" in a command is the key on a line of its own.
func appendStrings(out []string, raw json.RawMessage) []string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return out
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for k, e := range x {
				out = append(out, k)
				walk(e)
			}
		}
	}
	walk(v)
	return out
}
