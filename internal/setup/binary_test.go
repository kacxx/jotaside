package setup

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakeBinary(arg0 string) Binary {
	return Binary{
		Arg0:       arg0,
		LookPath:   func(string) (string, error) { return "", errors.New("not found") },
		Executable: func() (string, error) { return "", errors.New("no executable") },
		TempDir:    abs("/tmp"),
		Getwd:      func() (string, error) { return abs("/work"), nil },
		EvalLinks:  func(p string) (string, error) { return p, nil },
	}
}

// abs makes a slash path absolute on this platform (adds a drive on Windows).
func abs(p string) string {
	a, _ := filepath.Abs(filepath.FromSlash(p))
	return a
}

func TestResolveAbsoluteAsInvoked(t *testing.T) {
	want := abs("/home/u/go/bin/jotaside")
	got, err := fakeBinary(want).Resolve()
	if err != nil || got != want {
		t.Fatalf("Resolve = %q, %v; want %q", got, err, want)
	}
}

func TestResolveBareNameUsesLookPath(t *testing.T) {
	b := fakeBinary("jotaside")
	found := abs("/usr/local/bin/jotaside")
	b.LookPath = func(name string) (string, error) {
		if name != "jotaside" {
			t.Errorf("LookPath(%q)", name)
		}
		return found, nil
	}
	// os.Executable would give the resolved target; it must not be used.
	b.Executable = func() (string, error) { return abs("/opt/jotaside-1.2/jotaside"), nil }
	if got, err := b.Resolve(); err != nil || got != found {
		t.Fatalf("Resolve = %q, %v; want %q", got, err, found)
	}
}

func TestResolveRelativeIsMadeAbsolute(t *testing.T) {
	got, err := fakeBinary(filepath.FromSlash("./bin/jotaside")).Resolve()
	if want := abs("/work/bin/jotaside"); err != nil || got != want {
		t.Fatalf("Resolve = %q, %v; want %q", got, err, want)
	}
}

func TestResolveFallsBackToExecutable(t *testing.T) {
	b := fakeBinary("jotaside") // LookPath fails
	b.Executable = func() (string, error) { return abs("/opt/jotaside"), nil }
	if got, err := b.Resolve(); err != nil || got != abs("/opt/jotaside") {
		t.Fatalf("Resolve = %q, %v", got, err)
	}
	b.Executable = func() (string, error) { return "", errors.New("boom") }
	if _, err := b.Resolve(); err == nil {
		t.Fatal("want an error when nothing finds the binary")
	}
}

func TestResolveRefusesTemporaryBinary(t *testing.T) {
	for _, p := range []string{
		"/tmp/go-build123/b001/exe/jotaside", // go run
		"/var/folders/xx/T/go-build9/b001/exe/jotaside",
		"/tmp/x/jotaside", // under the temp dir
	} {
		_, err := fakeBinary(abs(p)).Resolve()
		if err == nil || !strings.Contains(err.Error(), "go install") {
			t.Errorf("%s: err = %v, want a refusal that says to go install", p, err)
		}
	}
	// A path that only resolves into the temp dir (macOS /var -> /private/var).
	b := fakeBinary(abs("/var/folders/jotaside"))
	b.TempDir = abs("/var/folders")
	b.EvalLinks = func(p string) (string, error) { return strings.Replace(p, abs("/var"), abs("/private/var"), 1), nil }
	if _, err := b.Resolve(); err == nil {
		t.Error("symlinked temp dir not detected")
	}
	// Not under the temp dir, and not a sibling with the same prefix.
	b = fakeBinary(abs("/tmpfoo/jotaside"))
	if _, err := b.Resolve(); err != nil {
		t.Errorf("/tmpfoo refused: %v", err)
	}
}

// A symlinked install is written as invoked, not as its target.
func TestResolveKeepsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "versions", "1.2", "jotaside")
	link := filepath.Join(dir, "bin", "jotaside")
	must(t, os.MkdirAll(filepath.Dir(target), 0o755))
	must(t, os.MkdirAll(filepath.Dir(link), 0o755))
	must(t, os.WriteFile(target, []byte("x"), 0o755))
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	b := Binary{Arg0: link, TempDir: filepath.Join(dir, "elsewhere")}
	if got, err := b.Resolve(); err != nil || got != link {
		t.Fatalf("Resolve = %q, %v; want the link %q", got, err, link)
	}
}
