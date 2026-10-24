// Package setup installs jotaside's prompt hook into an agent's config file
// without hand-editing JSON: it finds the binary's path, quotes it for the
// hook's command string, and merges the hook into the existing config.
package setup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Binary describes how to find the running binary. The zero value uses the
// real process; tests fill in the fields.
type Binary struct {
	Arg0       string                       // os.Args[0]
	LookPath   func(string) (string, error) // exec.LookPath
	Executable func() (string, error)       // os.Executable
	TempDir    string                       // os.TempDir()
	Getwd      func() (string, error)       // os.Getwd
	EvalLinks  func(string) (string, error) // filepath.EvalSymlinks, only to spot temp dirs
}

// Resolve returns the absolute path jotaside was invoked as. It deliberately does
// not resolve symlinks: a resolved path pins one exact install and breaks when
// the tool is reinstalled or upgraded through a symlink. os.Executable alone
// can't give this, because on Linux it reads /proc/self/exe, which has
// symlinks already resolved; it is only the fallback.
//
// It refuses a binary in a temporary directory (what `go run` builds into),
// since a hook pointing there disappears.
func (b Binary) Resolve() (string, error) {
	lookPath, executable, getwd, evalLinks := b.LookPath, b.Executable, b.Getwd, b.EvalLinks
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if executable == nil {
		executable = os.Executable
	}
	if getwd == nil {
		getwd = os.Getwd
	}
	if evalLinks == nil {
		evalLinks = filepath.EvalSymlinks
	}
	tmp := b.TempDir
	if tmp == "" {
		tmp = os.TempDir()
	}

	p, err := invokedPath(b.Arg0, lookPath, getwd)
	if err != nil {
		var err2 error
		if p, err2 = executable(); err2 != nil {
			return "", fmt.Errorf("cannot find the jotaside binary: %w", errors.Join(err, err2))
		}
		if !filepath.IsAbs(p) {
			return "", fmt.Errorf("cannot find the jotaside binary: %q is not absolute", p)
		}
	}
	p = filepath.Clean(p)
	if isTemporary(p, tmp, evalLinks) {
		return "", fmt.Errorf("refusing to write %s: it is in a temporary directory (`go run` builds there), so the hook would stop working; move the binary to a folder it will stay in, or run `go install github.com/kacxx/jotaside/cmd/jotaside@latest`, and run setup from there", p)
	}
	return p, nil
}

// invokedPath makes arg0 absolute without following symlinks.
func invokedPath(arg0 string, lookPath func(string) (string, error), getwd func() (string, error)) (string, error) {
	if arg0 == "" {
		return "", errors.New("no program name")
	}
	p := arg0
	if !strings.ContainsAny(p, `/\`) {
		found, err := lookPath(p)
		// ErrDot means the match is in the current directory; the path is
		// still the right one once made absolute.
		if err != nil && !errors.Is(err, exec.ErrDot) {
			return "", err
		}
		p = found
	}
	if filepath.IsAbs(p) {
		return p, nil
	}
	wd, err := getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(wd, p), nil
}

// isTemporary reports whether p is under tmp or has a go-build path element.
// It compares the symlink-resolved forms too, since macOS's temp directory is
// a symlink (/var/folders is /private/var/folders).
func isTemporary(p, tmp string, evalLinks func(string) (string, error)) bool {
	for _, el := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if strings.HasPrefix(el, "go-build") {
			return true
		}
	}
	tmps := []string{filepath.Clean(tmp)}
	if r, err := evalLinks(tmp); err == nil {
		tmps = append(tmps, filepath.Clean(r))
	}
	cands := []string{p}
	if r, err := evalLinks(p); err == nil {
		cands = append(cands, filepath.Clean(r))
	}
	for _, c := range cands {
		for _, t := range tmps {
			if within(c, t) {
				return true
			}
		}
	}
	return false
}

func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Quote quotes path for a hook's command string when it contains anything but
// safe characters. On Windows it also switches to forward slashes, which the
// hook shells accept and which need no escaping in JSON.
func Quote(path string, windows bool) string {
	if windows {
		path = strings.ReplaceAll(path, `\`, "/")
	}
	if path != "" && strings.IndexFunc(path, func(r rune) bool { return !safeRune(r) }) < 0 {
		return path
	}
	if windows {
		// Windows paths can't contain a double quote.
		return `"` + path + `"`
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

func safeRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("_@%+=:,./-", r)
}
