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
	"time"

	"github.com/kacxx/jotaside/internal/app"
)

// The log's grammar is in docs/worklog-format.md. In short:
//
//	## <title>                          a workstream
//	id: <slug> · opened: <date> · …     its metadata line; id is its stable name
//	- [ ] <text> — <fact> · <fact>      an open item
//	- [x] <text> — done <date> · …      a done item
//
// The facts follow the line's last " — " and are separated by "·": me,
// @person, since/due/done <ISO date>, ticket keys or URLs, and chat:<id> for
// a session that worked on the item. Everything else on a line is kept as it
// is, and a line that doesn't parse is never rewritten.
const (
	headingMark = "## "
	metaMark    = "id: "
	factsMark   = " — "
	factSep     = "·"
)

var (
	itemLine = regexp.MustCompile(`^- \[([ x])\] (.*)$`)
	chatFact = regexp.MustCompile(`^(?i)chat:([0-9a-f][0-9a-f-]{7,35})$`)
	keyShape = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[0-9]+$`)
	repoRef  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+#[0-9]+$`)
)

// Item is one action item in the log.
type Item struct {
	Line         int    // index into the log's lines
	Workstream   string // its workstream's title
	WorkstreamID string // and id; "" when the workstream has no metadata line
	Open         bool
	Text         string // the line after its marker
}

type factKind int

const (
	factOther  factKind = iota // not one the grammar names; kept as it is
	factMe                     // me
	factPerson                 // @person, or @person since <date>
	factSince                  // since <date>
	factDue                    // due <date>
	factDone                   // done <date>
	factChat                   // chat:<id>
	factKeys                   // one or more ticket keys or URLs
)

// kindOf returns what a fact is, and its date when it carries one.
func kindOf(f string) (factKind, string) {
	switch {
	case f == "me":
		return factMe, ""
	case strings.HasPrefix(f, "@"):
		if _, date, ok := strings.Cut(f, " since "); ok {
			return factPerson, date
		}
		return factPerson, ""
	case strings.HasPrefix(f, "since "):
		return factSince, strings.TrimPrefix(f, "since ")
	case strings.HasPrefix(f, "due "):
		return factDue, strings.TrimPrefix(f, "due ")
	case strings.HasPrefix(f, "done "):
		return factDone, strings.TrimPrefix(f, "done ")
	case chatFact.MatchString(f):
		return factChat, ""
	}
	words := strings.Fields(f)
	for _, w := range words {
		if !keyShape.MatchString(w) && !repoRef.MatchString(w) && !chatFact.MatchString(w) &&
			!strings.HasPrefix(w, "https://") && !strings.HasPrefix(w, "http://") {
			return factOther, ""
		}
	}
	if len(words) == 0 {
		return factOther, ""
	}
	return factKeys, ""
}

func validDate(d string) bool {
	_, err := time.Parse("2006-01-02", d)
	return err == nil
}

// splitFacts splits an item's text into its description and facts. The facts
// follow the last " — "; a tail in which no fact is one the grammar names is
// part of the description ("Phase A — parser rewrite").
func splitFacts(text string) (desc string, facts []string) {
	at := strings.LastIndex(text, factsMark)
	if at < 0 {
		return strings.TrimSpace(text), nil
	}
	known := false
	for _, f := range strings.Split(text[at+len(factsMark):], factSep) {
		if f = strings.TrimSpace(f); f != "" {
			facts = append(facts, f)
			kind, _ := kindOf(f)
			known = known || kind != factOther
		}
	}
	if !known {
		return strings.TrimSpace(text), nil
	}
	return strings.TrimSpace(text[:at]), facts
}

// Problem returns why the item doesn't parse, or "": a date that isn't an
// ISO date, or a done item with no done date.
func (it Item) Problem() string {
	_, facts := splitFacts(it.Text)
	done := false
	for _, f := range facts {
		kind, date := kindOf(f)
		done = done || kind == factDone
		if date != "" && !validDate(date) {
			return fmt.Sprintf("%q isn't a date (YYYY-MM-DD)", date)
		}
		if date == "" && (kind == factSince || kind == factDue || kind == factDone) {
			return fmt.Sprintf("%q has no date", f)
		}
	}
	if !it.Open && !done {
		return "it is done but has no done date"
	}
	return ""
}

// Stamps returns the session IDs (or their leading characters) in the item's
// chat: facts, lower-cased, in the order written, without repeats.
func (it Item) Stamps() []string {
	var out []string
	seen := map[string]bool{}
	_, facts := splitFacts(it.Text)
	for _, f := range facts {
		// Existing logs put several space-separated links in one fact.
		for _, w := range strings.Fields(f) {
			m := chatFact.FindStringSubmatch(w)
			if m == nil {
				continue
			}
			if id := strings.ToLower(m[1]); !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// Keys returns the work items the item names anywhere on its line (a tracker
// key, owner/repo#N or a GitHub issue or PR URL), in order, without repeats.
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

// workstream is one "## " section of the log.
type workstream struct {
	start, end int // its heading's line, and the line after its last
	title, id  string
}

func (w workstream) name() string {
	if w.id == "" {
		return w.title
	}
	return w.title + " (" + w.id + ")"
}

func (l log) workstreams() []workstream {
	var out []workstream
	for i, line := range l.lines {
		switch {
		case strings.HasPrefix(line, headingMark):
			if n := len(out); n > 0 {
				out[n-1].end = i
			}
			out = append(out, workstream{start: i, end: len(l.lines), title: strings.TrimSpace(line[len(headingMark):])})
		case strings.HasPrefix(line, "# ") && len(out) > 0 && out[len(out)-1].end == len(l.lines):
			out[len(out)-1].end = i
		case strings.HasPrefix(line, metaMark) && len(out) > 0 && out[len(out)-1].end == len(l.lines) && out[len(out)-1].id == "":
			id, _, _ := strings.Cut(line[len(metaMark):], factSep)
			out[len(out)-1].id = strings.TrimSpace(id)
		}
	}
	return out
}

// Items returns the log's items in order.
func (l log) Items() []Item {
	var items []Item
	wss := l.workstreams()
	w := -1
	for i, line := range l.lines {
		for w+1 < len(wss) && wss[w+1].start <= i {
			w++
		}
		m := itemLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		it := Item{Line: i, Open: m[1] == " ", Text: m[2]}
		if w >= 0 && i < wss[w].end {
			it.Workstream, it.WorkstreamID = wss[w].title, wss[w].id
		}
		items = append(items, it)
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

// stampFact is the fact for a session ID: chat: and its first 8 characters.
func stampFact(sessionID string) string {
	id := strings.ToLower(strings.TrimSpace(sessionID))
	if !regexp.MustCompile(`^[0-9a-f][0-9a-f-]{7,35}$`).MatchString(id) {
		return ""
	}
	return "chat:" + id[:8]
}

// withStamp returns facts with the session's stamp added, unless a stamp on
// the item already names that session.
func withStamp(facts []string, sessionID string) []string {
	tok := stampFact(sessionID)
	if tok == "" {
		return facts
	}
	for _, s := range (Item{Text: factsMark + strings.Join(facts, " "+factSep+" ")}).Stamps() {
		if strings.HasPrefix(s, tok[len("chat:"):]) {
			return facts
		}
	}
	return append(facts, tok)
}

func itemText(desc string, facts []string) string {
	return desc + factsMark + strings.Join(facts, " "+factSep+" ")
}

// Add returns content with a new open item after the workstream's last item.
// text is the item's description, optionally followed by " — " and its facts
// (an owner, a due date, ticket keys). The item gets an owner (me, unless
// the facts name a person, who gets "since <today>" if they have no date)
// and, when sessionID is one, a chat: stamp.
func Add(content, workstream, text, sessionID, today string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("nothing to add: the item has no text")
	}
	if strings.ContainsAny(text, "\r\n") {
		return "", errors.New("an item is one line")
	}
	// The leading space lets facts with nothing before them be seen as facts.
	desc, given := splitFacts(" " + text)
	if desc == "" {
		return "", errors.New("nothing to add: the item has no text before its facts")
	}
	var owner string
	var rest []string
	dated := false
	for _, f := range given {
		kind, date := kindOf(f)
		switch {
		case kind == factDone:
			return "", errors.New("add writes an open item; add it, then close it")
		case date != "" && !validDate(date):
			return "", fmt.Errorf("%q isn't a date (YYYY-MM-DD)", date)
		case date == "" && (kind == factSince || kind == factDue):
			return "", fmt.Errorf("%q has no date", f)
		case kind == factMe || kind == factPerson:
			if owner != "" {
				return "", fmt.Errorf("an item has one owner, and this names two: %s, %s", owner, f)
			}
			owner, dated = f, date != ""
		default:
			dated = dated || kind == factSince
			rest = append(rest, f)
		}
	}
	switch {
	case owner == "":
		owner = "me"
	case owner != "me" && !dated:
		owner += " since " + today
	}
	facts := withStamp(append([]string{owner}, rest...), sessionID)

	l := parse(content)
	ws, err := l.find(workstream)
	if err != nil {
		return "", err
	}
	// After the section's last item; with no items yet, after its last line
	// that isn't blank.
	at, lastItem := ws.start, -1
	for i := ws.start + 1; i < ws.end; i++ {
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
	line := "- [ ] " + itemText(desc, facts)
	l.lines = append(l.lines[:at+1], append([]string{line}, l.lines[at+1:]...)...)
	return l.String(), nil
}

// find returns the workstream that name asks for: the one with that id, else
// the one with that title, else the only one whose id or title contains it,
// all in any case.
func (l log) find(name string) (workstream, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return workstream{}, errors.New("name the workstream to add to")
	}
	var byID, byTitle, partial []workstream
	var names []string
	for _, w := range l.workstreams() {
		names = append(names, w.name())
		id, title := strings.ToLower(w.id), strings.ToLower(w.title)
		switch {
		case id == name:
			byID = append(byID, w)
		case title == name:
			byTitle = append(byTitle, w)
		case strings.Contains(id, name) || strings.Contains(title, name):
			partial = append(partial, w)
		}
	}
	for _, found := range [][]workstream{byID, byTitle, partial} {
		switch len(found) {
		case 0:
			continue
		case 1:
			return found[0], nil
		default:
			return workstream{}, fmt.Errorf("%q matches more than one workstream; use its id", name)
		}
	}
	if len(names) == 0 {
		return workstream{}, errors.New("the log has no workstream (a \"## \" heading) to add to")
	}
	return workstream{}, fmt.Errorf("no workstream matches %q; the log has: %s", name, strings.Join(names, "; "))
}

// Close returns content with the one open item matching q marked done, and
// that item as it now reads. The item gets "done <today>" and, when
// sessionID is one, a chat: stamp; it loses "me" and its due date, and keeps
// a person it was waiting on, its keys and its stamps.
func Close(content, q, sessionID, today string) (string, Item, error) {
	l := parse(content)
	var open []Item
	for _, it := range l.Items() {
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
	if p := it.Problem(); p != "" {
		return "", Item{}, fmt.Errorf("line %d doesn't parse (%s), so it was left as it is; fix it by hand:\n  %s", it.Line+1, p, l.lines[it.Line])
	}
	desc, was := splitFacts(it.Text)
	var who, rest []string
	for _, f := range was {
		switch kind, _ := kindOf(f); kind {
		case factMe, factDue, factDone:
		case factPerson, factSince:
			who = append(who, f)
		default:
			rest = append(rest, f)
		}
	}
	facts := withStamp(append(append(who, "done "+today), rest...), sessionID)
	it.Open, it.Text = false, itemText(desc, facts)
	l.lines[it.Line] = "- [x] " + it.Text
	return l.String(), it, nil
}
