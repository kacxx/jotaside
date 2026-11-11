// Package worklog links a markdown worklog's items to the agent sessions
// that wrote them (kacxx/notebook#35). It adds and closes items, stamping
// each with the calling session's ID, and looks an item's sessions up again
// with a command that resumes each.
package worklog

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/kacxx/jotaside/internal/app"
)

// The log's grammar, as far as this package reads and writes it. Everything
// else on a line is kept as it is.
//
//	## <workstream>     a heading (level 2 or deeper) starts a workstream
//	- [ ] <text>        an open item
//	- [x] <text>        a done item
//	chat:<id>           on an item: a session that wrote it, as the first 8
//	                    characters of its ID or more
//
// ASSUMED, not checked: the log this was built for defines its grammar in a
// document this code was written without. Only the chat: token is confirmed.
// If headings or item markers differ, this is the file to change; add and
// close refuse to write to a log in which they recognise no item at all.
var (
	headingLine = regexp.MustCompile(`^#{2,6}\s+(.*\S)\s*$`)
	itemLine    = regexp.MustCompile(`^(\s*[-*] \[)([ xX])(\] )(.*)$`)
	// What follows the ID is checked in Stamps: matching it here would use up
	// the space before a second token.
	chatToken = regexp.MustCompile(`(?:^|\s)chat:([0-9A-Fa-f][0-9A-Fa-f-]{7,35})`)
	keyShape  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[0-9]+$`)
)

// Item is one action item in the log.
type Item struct {
	Line       int // index into the log's lines
	Workstream string
	Open       bool
	Text       string // the line after its marker
}

// Stamps returns the session IDs (or their leading characters) stamped on the
// item, lower-cased, in the order written, without repeats.
func (it Item) Stamps() []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range chatToken.FindAllStringSubmatchIndex(it.Text, -1) {
		if end := m[3]; end < len(it.Text) {
			// The ID must end the word: chat:0a1b2c3dxyz isn't a stamp.
			if c := it.Text[end]; c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
				continue
			}
		}
		if id := strings.ToLower(it.Text[m[2]:m[3]]); !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// Keys returns the work items the item names (a tracker key, owner/repo#N or
// a GitHub issue or PR URL), in order, without repeats.
func (it Item) Keys() []string {
	var out []string
	seen := map[string]bool{}
	rest := it.Text
	for {
		key, ok := app.WorkItem(rest)
		if !ok {
			return out
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
		// A URL is returned as owner/repo#N, which isn't in the text; its
		// number is, so skip past that.
		at := strings.Index(rest, key)
		if at < 0 {
			num := key[strings.LastIndexAny(key, "#-")+1:]
			at = strings.Index(rest, num)
			key = num
		}
		if at < 0 {
			return out
		}
		rest = rest[at+len(key):]
	}
}

// log is a parsed log: its lines, and the line ending to write them back with.
type log struct {
	lines []string
	eol   string
}

func parse(content string) log {
	eol := "\n"
	if strings.Contains(content, "\r\n") {
		eol = "\r\n"
	}
	return log{lines: strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n"), eol: eol}
}

func (l log) String() string { return strings.Join(l.lines, l.eol) }

// Items returns the log's items in order.
func (l log) Items() []Item {
	var items []Item
	ws := ""
	for i, line := range l.lines {
		if m := headingLine.FindStringSubmatch(line); m != nil {
			ws = m[1]
			continue
		}
		if m := itemLine.FindStringSubmatch(line); m != nil {
			items = append(items, Item{Line: i, Workstream: ws, Open: m[2] == " ", Text: m[4]})
		}
	}
	return items
}

// Items parses a log's content and returns its items.
func Items(content string) []Item { return parse(content).Items() }

// matches reports whether the item is one the query asks for. A query shaped
// like a ticket key matches only as a whole word, in any case; anything else
// matches as text, in any case.
func matches(it Item, q string) bool {
	q = strings.TrimSpace(q)
	if q == "" {
		return false
	}
	if keyShape.MatchString(q) {
		whole := regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])` + regexp.QuoteMeta(q) + `(?:[^A-Za-z0-9]|$)`)
		return whole.MatchString(it.Text)
	}
	return strings.Contains(strings.ToLower(it.Text), strings.ToLower(q))
}

// Find returns the items matching q, open and done.
func Find(content, q string) []Item {
	var out []Item
	for _, it := range Items(content) {
		if matches(it, q) {
			out = append(out, it)
		}
	}
	return out
}

var errNoItems = errors.New("no items recognised in the log, so nothing was written: its grammar may differ from the one this tool assumes (see internal/worklog/ledger.go)")

// stampToken is the token for a session ID: its first 8 characters.
func stampToken(sessionID string) string {
	id := strings.ToLower(strings.TrimSpace(sessionID))
	if !regexp.MustCompile(`^[0-9a-f][0-9a-f-]{7,35}$`).MatchString(id) {
		return ""
	}
	return "chat:" + id[:8]
}

// Add returns content with a new open item at the end of the workstream's
// items, stamped with sessionID when it has one.
func Add(content, workstream, text, sessionID string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("nothing to add: the item has no text")
	}
	if strings.ContainsAny(text, "\r\n") {
		return "", errors.New("an item is one line")
	}
	l := parse(content)
	if strings.TrimSpace(content) != "" && len(l.Items()) == 0 {
		return "", errNoItems
	}
	start, err := l.heading(workstream)
	if err != nil {
		return "", err
	}
	end := len(l.lines)
	for i := start + 1; i < len(l.lines); i++ {
		if headingLine.MatchString(l.lines[i]) || strings.HasPrefix(l.lines[i], "# ") {
			end = i
			break
		}
	}
	// After the section's last item; with no items yet, after its last line
	// that isn't blank.
	at, lastItem := start, -1
	for i := start + 1; i < end; i++ {
		if itemLine.MatchString(l.lines[i]) {
			lastItem = i
		}
		if strings.TrimSpace(l.lines[i]) != "" {
			at = i
		}
	}
	if lastItem >= 0 {
		at = lastItem
	}
	line := "- [ ] " + text
	if tok := stampToken(sessionID); tok != "" {
		line += " " + tok
	}
	l.lines = append(l.lines[:at+1], append([]string{line}, l.lines[at+1:]...)...)
	return l.String(), nil
}

// heading returns the line of the workstream heading that name asks for: the
// one with that name in any case, else the only one containing it.
func (l log) heading(name string) (int, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return 0, errors.New("name the workstream to add to")
	}
	var exact, partial []int
	var names []string
	for i, line := range l.lines {
		m := headingLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		names = append(names, m[1])
		h := strings.ToLower(m[1])
		if h == name {
			exact = append(exact, i)
		} else if strings.Contains(h, name) {
			partial = append(partial, i)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) == 0 && len(partial) == 1:
		return partial[0], nil
	case len(exact)+len(partial) == 0:
		return 0, fmt.Errorf("no workstream matches %q; the log has: %s", name, strings.Join(names, "; "))
	default:
		return 0, fmt.Errorf("%q matches more than one workstream; give more of its name", name)
	}
}

// Close returns content with the one open item matching q marked done and
// stamped with sessionID when it has one, and that item as it now reads.
func Close(content, q, sessionID string) (string, Item, error) {
	l := parse(content)
	items := l.Items()
	if len(items) == 0 {
		return "", Item{}, errNoItems
	}
	var open []Item
	for _, it := range items {
		if it.Open && matches(it, q) {
			open = append(open, it)
		}
	}
	switch len(open) {
	case 0:
		return "", Item{}, fmt.Errorf("no open item matches %q", q)
	case 1:
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%d open items match %q; give more of the one to close:", len(open), q)
		for _, it := range open {
			fmt.Fprintf(&b, "\n  - %s", it.Text)
		}
		return "", Item{}, errors.New(b.String())
	}
	it := open[0]
	m := itemLine.FindStringSubmatch(l.lines[it.Line])
	text := m[4]
	if tok := stampToken(sessionID); tok != "" {
		already := false
		for _, s := range it.Stamps() {
			already = already || strings.HasPrefix(s, tok[len("chat:"):])
		}
		if !already {
			text = strings.TrimRight(text, " \t") + " " + tok
		}
	}
	l.lines[it.Line] = m[1] + "x" + m[3] + text
	it.Open, it.Text = false, text
	return l.String(), it, nil
}
