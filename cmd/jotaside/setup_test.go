package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kacxx/jotaside/internal/setup"
)

// setupEnv points the agents' config at a throwaway home and pretends jotaside
// was invoked as bin.
func setupEnv(t *testing.T) (home, bin string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	bin, _ = filepath.Abs(filepath.Join("testdata-bin", "jotaside"))
	old, oldOS := setupBinary, setupGOOS
	setupBinary = func() setup.Binary {
		return setup.Binary{Arg0: bin, TempDir: filepath.Join(home, "no-temp")}
	}
	setupGOOS = "linux"
	t.Cleanup(func() { setupBinary, setupGOOS = old, oldOS })
	return home, bin
}

func runSetup(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(append([]string{"setup"}, args...), strings.NewReader(""), &out)
	return out.String(), err
}

func TestSetupClaude(t *testing.T) {
	home, bin := setupEnv(t)
	path := filepath.Join(home, ".claude", "settings.json")

	q := setup.Quote(bin, false)
	out, err := runSetup(t, "claude", "--dry-run", "--mcp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("--dry-run wrote the file")
	}
	for _, want := range []string{"Dry run", q + " hook claude", "docs/cursor.md", "claude mcp add --scope user jotaside -- " + q + " mcp", "not run"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "Dry run") || strings.Index(out, "Dry run") > strings.Index(out, "Cursor imports hooks") {
		t.Errorf("dry run should lead with the result, before the Cursor note:\n%s", out)
	}

	out, err = runSetup(t, "claude")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), strings.ReplaceAll(setup.Quote(bin, false), `\`, `\\`)+" hook claude") {
		t.Fatalf("settings.json:\n%s", b)
	}
	if !strings.Contains(out, "Added") || !strings.Contains(out, ">> test") || !strings.Contains(out, "Cursor imports hooks") {
		t.Errorf("output:\n%s", out)
	}
	if !strings.Contains(out, "✓ Added") || strings.Index(out, "✓ Added") > strings.Index(out, "Cursor imports hooks") {
		t.Errorf("checkmark should lead, before the Cursor note:\n%s", out)
	}
	if strings.Contains(out, "claude mcp add") {
		t.Errorf("printed the MCP command without --mcp:\n%s", out)
	}

	// Running again changes nothing and makes no backup.
	out, err = runSetup(t, "claude")
	if err != nil || !strings.Contains(out, "nothing changed") {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	if m, _ := filepath.Glob(path + ".bak-*"); len(m) != 0 {
		t.Errorf("backups after a no-op: %v", m)
	}
}

func TestSetupCodexTrustReminder(t *testing.T) {
	home, bin := setupEnv(t)
	path := filepath.Join(home, ".codex", "hooks.json")

	out, err := runSetup(t, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/hooks") || !strings.Contains(out, "trust") {
		t.Errorf("first install doesn't mention trusting the hook:\n%s", out)
	}
	if strings.Contains(out, "Cursor") {
		t.Errorf("codex output mentions Cursor:\n%s", out)
	}

	// The path changes: trusting is needed again.
	old := setupBinary
	other := filepath.Join(filepath.Dir(bin), "moved", "jotaside")
	setupBinary = func() setup.Binary { b := old(); b.Arg0 = other; return b }
	out, err = runSetup(t, "codex", "--mcp")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Updated", "replaced: " + setup.Quote(bin, false) + " hook codex", "trust it", "again", "codex mcp add jotaside -- " + setup.Quote(other, false) + " mcp"} {
		if !strings.Contains(out, want) {
			t.Errorf("update output lacks %q:\n%s", want, out)
		}
	}
	if m, _ := filepath.Glob(path + ".bak-*"); len(m) != 1 {
		t.Errorf("want one backup, got %v", m)
	}
	// The same path again: nothing to trust.
	out, _ = runSetup(t, "codex")
	if strings.Contains(out, "trust it") {
		t.Errorf("no-op run asks to trust again:\n%s", out)
	}
}

func TestSetupQuotesPathWithSpace(t *testing.T) {
	home, _ := setupEnv(t)
	spaced := filepath.Join(home, "John Smith", "go", "bin", "jotaside")
	setupBinary = func() setup.Binary { return setup.Binary{Arg0: spaced, TempDir: filepath.Join(home, "no-temp")} }
	out, err := runSetup(t, "claude", "--mcp")
	if err != nil {
		t.Fatal(err)
	}
	q := setup.Quote(spaced, false)
	if !strings.HasPrefix(q, "'") || !strings.Contains(out, "command: "+q+" hook claude") || !strings.Contains(out, "-- "+q+" mcp") {
		t.Errorf("path not quoted (want %s):\n%s", q, out)
	}
}

func TestSetupDryRunHidesRestOfFile(t *testing.T) {
	home, _ := setupEnv(t)
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"env":{"TOKEN":"s3cret"},"hooks":{"UserPromptSubmit":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runSetup(t, "claude", "--dry-run")
	if err != nil || strings.Contains(out, "s3cret") || !strings.Contains(out, "UserPromptSubmit") {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestSetupRenamedBinaryIsIdempotent(t *testing.T) {
	home, _ := setupEnv(t)
	dev := filepath.Join(home, "bin", "jotaside-dev")
	if err := os.MkdirAll(filepath.Dir(dev), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dev, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	setupBinary = func() setup.Binary { return setup.Binary{Arg0: dev, TempDir: filepath.Join(home, "no-temp")} }
	for i, want := range []string{"Added", "nothing changed", "nothing changed"} {
		out, err := runSetup(t, "claude")
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("run %d: %v\n%s", i, err, out)
		}
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if strings.Count(string(b), "hook claude") != 1 {
		t.Errorf("settings.json:\n%s", b)
	}
}

func TestSetupCursorWritesNothing(t *testing.T) {
	home, _ := setupEnv(t)
	out, err := runSetup(t, "cursor")
	if err != nil || !strings.Contains(out, "docs/cursor.md") || !strings.Contains(out, "isn't supported") {
		t.Fatalf("%v\n%s", err, out)
	}
	if m, _ := filepath.Glob(filepath.Join(home, ".*")); len(m) != 0 {
		t.Errorf("wrote %v", m)
	}
}

func TestSetupErrors(t *testing.T) {
	home, _ := setupEnv(t)
	for _, args := range [][]string{{}, {"vim"}, {"claude", "codex"}, {"claude", "--nope"}} {
		if _, err := runSetup(t, args...); err == nil {
			t.Errorf("jotaside setup %v: want an error", args)
		}
	}
	// A config that doesn't parse is left alone.
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{ nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runSetup(t, "claude"); err == nil || !strings.Contains(err.Error(), "settings.json") {
		t.Errorf("err = %v, want one naming the file", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "{ nope" {
		t.Errorf("file changed: %q", b)
	}
	// A temporary binary is refused.
	setupBinary = func() setup.Binary { return setup.Binary{Arg0: filepath.Join(os.TempDir(), "go-build1", "jotaside")} }
	if _, err := runSetup(t, "claude"); err == nil || !strings.Contains(err.Error(), "go install") {
		t.Errorf("err = %v, want a go install hint", err)
	}
}

// Upgrading from aside (or jot): setup replaces the old hook with exactly one
// jotaside hook, keeps everything else, writes a backup, and says how to
// replace an MCP server still registered under the old name.
func TestSetupReplacesPreviousNames(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			home, bin := setupEnv(t)
			var path, mcpPath, mcpCfg, remove string
			if agent == "claude" {
				path = filepath.Join(home, ".claude", "settings.json")
				mcpPath = filepath.Join(home, ".claude.json")
				mcpCfg = `{"mcpServers":{"aside":{"command":"/Users/x/go/bin/aside","args":["mcp"]}}}`
				remove = "claude mcp remove --scope user aside"
			} else {
				path = filepath.Join(home, ".codex", "hooks.json")
				mcpPath = filepath.Join(home, ".codex", "config.toml")
				mcpCfg = "[mcp_servers.aside]\ncommand = \"/Users/x/go/bin/aside\"\nargs = [\"mcp\"]\n"
				remove = "codex mcp remove aside"
			}
			old := `{"model":"x","hooks":{"UserPromptSubmit":[
  {"hooks":[{"type":"command","command":"/usr/local/bin/other-tool prompt"}]},
  {"hooks":[{"type":"command","command":"/x/aside hook ` + agent + `"}]},
  {"hooks":[{"type":"command","command":"jot hook ` + agent + `"}]}]}}`
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(mcpPath, []byte(mcpCfg), 0o600); err != nil {
				t.Fatal(err)
			}

			out, err := runSetup(t, agent)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(path)
			got := string(b)
			q := setup.Quote(bin, false)
			// The config is JSON, so a Windows path's backslashes are escaped in it.
			want, _ := json.Marshal(q + " hook " + agent)
			if strings.Count(got, " hook "+agent) != 1 || !strings.Contains(got, string(want[1:len(want)-1])) ||
				!strings.Contains(got, "/usr/local/bin/other-tool prompt") || !strings.Contains(got, `"model": "x"`) ||
				strings.Contains(got, "/x/aside") || strings.Contains(got, `"jot hook`) {
				t.Errorf("config:\n%s", got)
			}
			if m, _ := filepath.Glob(path + ".bak-*"); len(m) != 1 {
				t.Errorf("want one backup, got %v", m)
			} else if bak, _ := os.ReadFile(m[0]); string(bak) != old {
				t.Errorf("backup is not the old file:\n%s", bak)
			}
			for _, want := range []string{"replaced: /x/aside hook " + agent, "removed 1 duplicate jotaside entry",
				mcpPath + " registers the jot MCP server under an old name", "aside (runs /Users/x/go/bin/aside)", remove, "jotaside -- " + q + " mcp"} {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "To give") {
				t.Errorf("printed both the replace and the plain add command:\n%s", out)
			}
			if b, _ := os.ReadFile(mcpPath); string(b) != mcpCfg {
				t.Errorf("setup changed the MCP config:\n%s", b)
			}
		})
	}
}
