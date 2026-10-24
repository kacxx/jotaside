package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
)

const event = "UserPromptSubmit"

// Hook is the jotaside entry to install.
type Hook struct {
	Agent   string // "claude" or "codex"
	Command string // the command string, already quoted
	// Binary is the unquoted path of this binary. An entry that runs it is
	// jotaside's whatever the file is called, so a renamed or side-by-side build
	// (jotaside-dev) updates its own hook instead of stacking another.
	Binary string
	// Windows also sets "commandWindows", which Codex prefers on Windows. It
	// is left alone on other platforms, so a config shared between machines
	// keeps the Windows command.
	Windows bool
}

// Change says what Merge did to the config.
type Change struct {
	Kind     string   // "added", "updated" or "unchanged"
	Replaced []string // commands of the entries that were replaced
	Removed  int      // extra jotaside entries removed, so only one runs
}

// Merge adds h to the UserPromptSubmit hooks in data (the contents of a
// settings.json or hooks.json; empty means no config yet) and returns the new
// contents. Everything else in the file keeps its order and content; only
// whitespace can change. An existing jotaside entry, including one under an
// earlier name (`aside hook claude`, `jot hook claude`), is updated instead of
// duplicated.
func Merge(data []byte, h Hook) ([]byte, Change, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	if !json.Valid(data) {
		var v any
		return nil, Change{}, fmt.Errorf("not valid JSON: %w", json.Unmarshal(data, &v))
	}
	top, err := parseObject(data)
	if err != nil {
		return nil, Change{}, fmt.Errorf("expected a JSON object at the top level")
	}
	hooks := &object{vals: map[string]json.RawMessage{}}
	if raw, ok := top.vals["hooks"]; ok {
		if hooks, err = parseObject(raw); err != nil {
			return nil, Change{}, errors.New(`"hooks" is not an object`)
		}
	}
	var groups []json.RawMessage
	if raw, ok := hooks.vals[event]; ok {
		if err := json.Unmarshal(raw, &groups); err != nil {
			return nil, Change{}, fmt.Errorf("%q is not an array", event)
		}
	}

	var ch Change
	found, modified := false, false
	for gi := 0; gi < len(groups); gi++ {
		g, err := parseObject(groups[gi])
		if err != nil {
			continue // not ours to interpret
		}
		var handlers []json.RawMessage
		if raw, ok := g.vals["hooks"]; !ok || json.Unmarshal(raw, &handlers) != nil {
			continue
		}
		groupChanged := false
		for hi := 0; hi < len(handlers); hi++ {
			e, err := parseObject(handlers[hi])
			if err != nil || !isOurs(e, h) {
				continue
			}
			if found {
				handlers = append(handlers[:hi], handlers[hi+1:]...)
				hi--
				ch.Removed++
				groupChanged, modified = true, true
				continue
			}
			found = true
			old := stringField(e, "command")
			ch.Replaced = append(ch.Replaced, old)
			changed := e.setString("command", h.Command)
			if h.Windows {
				changed = e.setString("commandWindows", h.Command) || changed
			}
			if changed {
				handlers[hi] = e.marshal()
				groupChanged, modified = true, true
			}
		}
		if !groupChanged {
			continue
		}
		if len(handlers) == 0 && onlyKeys(g, "hooks", "matcher") {
			// Nothing left to run, and nothing else in the group to keep.
			groups = append(groups[:gi], groups[gi+1:]...)
			gi--
			continue
		}
		g.vals["hooks"] = marshalArray(handlers)
		groups[gi] = g.marshal()
	}

	switch {
	case !found:
		ch.Kind = "added"
		e := &object{vals: map[string]json.RawMessage{}}
		e.setString("type", "command")
		e.setString("command", h.Command)
		if h.Windows {
			e.setString("commandWindows", h.Command)
		}
		g := &object{vals: map[string]json.RawMessage{}}
		g.set("hooks", marshalArray([]json.RawMessage{e.marshal()}))
		groups = append(groups, g.marshal())
	case !modified:
		return data, Change{Kind: "unchanged"}, nil
	default:
		ch.Kind = "updated"
	}

	hooks.set(event, marshalArray(groups))
	top.set("hooks", hooks.marshal())
	var out bytes.Buffer
	if err := json.Indent(&out, top.marshal(), "", "  "); err != nil {
		return nil, Change{}, err
	}
	out.WriteByte('\n')
	return out.Bytes(), ch, nil
}

// isOurs reports whether handler e runs `jotaside hook <agent>`, or the same
// under an earlier name of the command (`aside hook <agent>`, `jot hook
// <agent>`), in either command field.
func isOurs(e *object, h Hook) bool {
	for _, k := range []string{"command", "commandWindows"} {
		if isOurCommand(stringField(e, k), h) {
			return true
		}
	}
	return false
}

func isOurCommand(cmd string, h Hook) bool {
	f := splitCommand(cmd)
	if len(f) != 3 || f[1] != "hook" || f[2] != h.Agent {
		return false
	}
	return isOurProgram(f[0], h.Binary)
}

// previousNames are earlier names of the jotaside command, oldest first. A
// hook that runs one of them is replaced by setup rather than left to run
// next to the new one.
var previousNames = []string{"jot", "aside"}

// isOurProgram reports whether prog is jotaside (or one of its previous
// names), or is this very binary: the same path or, through links, the same
// file.
func isOurProgram(prog, bin string) bool {
	if bin != "" {
		if prog == bin || strings.ReplaceAll(prog, `\`, "/") == strings.ReplaceAll(bin, `\`, "/") {
			return true
		}
		if a, err := os.Stat(prog); err == nil {
			if b, err := os.Stat(bin); err == nil && os.SameFile(a, b) {
				return true
			}
		}
	}
	name := prog
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	return name == "jotaside" || slices.Contains(previousNames, name)
}

// splitCommand splits a command string on spaces, honouring single and double
// quotes. A backslash is literal, so Windows paths survive, except before a
// quote outside quotes, which is how POSIX quoting writes a single quote.
func splitCommand(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	in, escaped := false, false
	for _, r := range s {
		switch {
		case escaped:
			escaped = false
			if r != '\'' && r != '"' {
				cur.WriteRune('\\')
			}
			cur.WriteRune(r)
			in = true
		case r == '\\' && quote == 0:
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, in = r, true
		case r == ' ' || r == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if escaped {
		cur.WriteRune('\\')
		in = true
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

func onlyKeys(o *object, allowed ...string) bool {
	for _, k := range o.keys {
		if !slices.Contains(allowed, k) {
			return false
		}
	}
	return true
}

// Section returns just the hooks.UserPromptSubmit part of a config produced
// by Merge, indented. A dry run shows this instead of the whole file, which
// can hold env values and tokens that don't belong in a terminal or an
// agent's context.
func Section(config []byte) ([]byte, error) {
	top, err := parseObject(config)
	if err != nil {
		return nil, err
	}
	hooks, err := parseObject(top.vals["hooks"])
	if err != nil {
		return nil, err
	}
	hooksOnly := &object{vals: map[string]json.RawMessage{}}
	hooksOnly.set(event, hooks.vals[event])
	wrap := &object{vals: map[string]json.RawMessage{}}
	wrap.set("hooks", hooksOnly.marshal())
	var out bytes.Buffer
	if err := json.Indent(&out, wrap.marshal(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func stringField(o *object, k string) string {
	var s string
	if raw, ok := o.vals[k]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

// object is a JSON object that remembers key order and keeps values raw, so
// editing one key leaves the rest of the file as it was.
type object struct {
	keys []string
	vals map[string]json.RawMessage
}

func parseObject(raw []byte) (*object, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not an object")
	}
	o := &object{vals: map[string]json.RawMessage{}}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if _, dup := o.vals[k]; dup {
			return nil, fmt.Errorf("duplicate key %q", k)
		}
		o.keys = append(o.keys, k)
		o.vals[k] = v
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("trailing data")
	}
	return o, nil
}

func (o *object) set(k string, v json.RawMessage) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// setString sets k to the string v and reports whether that changed it.
func (o *object) setString(k, v string) bool {
	if stringField(o, k) == v {
		if _, ok := o.vals[k]; ok {
			return false
		}
	}
	o.set(k, jsonString(v))
	return true
}

func (o *object) marshal() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(jsonString(k))
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

func marshalArray(items []json.RawMessage) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(it)
	}
	b.WriteByte(']')
	return b.Bytes()
}

func jsonString(s string) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSpace(b.Bytes())
}
