package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kacxx/jotaside/internal/app"
)

func TestOpenCLI(t *testing.T) {
	t.Setenv("JOT_DB", filepath.Join(t.TempDir(), "jot.db"))
	const thread = "11111111-1111-4111-8111-111111111111"
	svc, err := app.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, req := range []app.CaptureRequest{
		{Text: "codex jot", Source: "codex", SessionID: thread},
		{Text: "claude jot", Source: "claude", SessionID: thread},
		{Text: "terminal jot", Source: "cli"},
	} {
		if _, err := svc.Capture(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	svc.Close()

	var opened []string
	oldOS, oldOpen := openGOOS, openURL
	t.Cleanup(func() { openGOOS, openURL = oldOS, oldOpen })
	openURL = func(_ context.Context, u string) error { opened = append(opened, u); return nil }
	do := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := run(args, strings.NewReader(""), &out)
		return out.String(), err
	}

	openGOOS = "darwin"
	out, err := do("open", "1")
	if err != nil || out != "opened codex://threads/"+thread+"\n" || len(opened) != 1 || opened[0] != "codex://threads/"+thread {
		t.Fatalf("out=%q err=%v opened=%v", out, err, opened)
	}
	// No link (a Claude jot not from Desktop): nothing opened, resume command shown.
	_, err = do("open", "2")
	if err == nil || !strings.Contains(err.Error(), "no link") || !strings.Contains(err.Error(), "claude --resume "+thread) || len(opened) != 1 {
		t.Fatalf("no link: %v opened=%v", err, opened)
	}
	// No session at all.
	if _, err = do("open", "3"); err == nil || !strings.Contains(err.Error(), "no link") || strings.Contains(err.Error(), "run:") {
		t.Fatalf("no session: %v", err)
	}
	if _, err = do("open", "99"); err == nil || !strings.Contains(err.Error(), "no jot #99") {
		t.Fatalf("missing: %v", err)
	}
	if _, err = do("open"); err == nil {
		t.Fatal("open with no id succeeded")
	}
	// Other platforms say so and show the link.
	openGOOS = "linux"
	if _, err = do("open", "1"); err == nil || !strings.Contains(err.Error(), "only supported on macOS and Windows") || len(opened) != 1 {
		t.Fatalf("linux: %v opened=%v", err, opened)
	}
	// A failing opener is reported.
	openGOOS = "darwin"
	openURL = func(context.Context, string) error { return errors.New("boom") }
	if _, err = do("open", "1"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("open failure: %v", err)
	}
}

// A Claude Desktop jot goes through jotaside open to its continue link, and an
// archived session gets none.
func TestOpenCLIDesktop(t *testing.T) {
	t.Setenv("JOT_DB", filepath.Join(t.TempDir(), "jot.db"))
	const (
		cli      = "11111111-1111-4111-8111-111111111111"
		cliGone  = "22222222-2222-4222-8222-222222222222"
		local    = "local_aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		localOld = "local_bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	dir := t.TempDir()
	for _, f := range []struct {
		local, cli string
		archived   bool
	}{{local, cli, false}, {localOld, cliGone, true}} {
		d := filepath.Join(dir, "a", "b")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"sessionId":%q,"cliSessionId":%q,"isArchived":%v}`, f.local, f.cli, f.archived)
		if err := os.WriteFile(filepath.Join(d, f.local+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldDir, oldOS, oldOpen := desktopSessionsDir, openGOOS, openURL
	t.Cleanup(func() { desktopSessionsDir, openGOOS, openURL = oldDir, oldOS, oldOpen })
	desktopSessionsDir, openGOOS = dir, "darwin"
	var opened []string
	openURL = func(_ context.Context, u string) error { opened = append(opened, u); return nil }

	svc, err := app.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"claude_entrypoint": "claude-desktop", "claude_desktop_session_id": local}
	for _, req := range []app.CaptureRequest{
		{Text: "desktop jot", Source: "claude", SessionID: cli, Metadata: meta},
		{Text: "archived jot", Source: "claude", SessionID: cliGone, Metadata: map[string]any{"claude_entrypoint": "claude-desktop"}},
	} {
		if _, err := svc.Capture(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	svc.Close()

	var out bytes.Buffer
	if err := run([]string{"open", "1"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	want := "claude://code/continue?session=" + local
	if out.String() != "opened "+want+"\n" || len(opened) != 1 || opened[0] != want {
		t.Fatalf("out=%q opened=%v", out.String(), opened)
	}
	if err := run([]string{"open", "2"}, strings.NewReader(""), &out); err == nil || !strings.Contains(err.Error(), "no link") || len(opened) != 1 {
		t.Fatalf("archived: %v opened=%v", err, opened)
	}

	// A later jot in the Desktop session has no metadata of its own; the
	// session's link still opens from it, and find prints the command for it.
	svc, err = app.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Capture(context.Background(), app.CaptureRequest{Text: "later desktop jot", Source: "claude", SessionID: cli}); err != nil {
		t.Fatal(err)
	}
	svc.Close()
	out.Reset()
	if err := run([]string{"open", "3"}, strings.NewReader(""), &out); err != nil || len(opened) != 2 || opened[1] != want {
		t.Fatalf("later jot: err=%v opened=%v", err, opened)
	}
	out.Reset()
	if err := run([]string{"find", "desktop jot"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\n  jotaside open 3\n") {
		t.Fatalf("find output has no open command for the newest jot:\n%s", out.String())
	}
	out.Reset()
	if err := run([]string{"sessions"}, strings.NewReader(""), &out); err != nil || !strings.Contains(out.String(), "\n  jotaside open 3\n") {
		t.Fatalf("sessions has no open command for the newest jot: err=%v\n%s", err, out.String())
	}
}

// A failing open reports what it printed.
func TestOpenWithKeepsOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script")
	}
	bin := filepath.Join(t.TempDir(), "open")
	script := "#!/bin/sh\necho 'No application knows how to open URL' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	err := openWith(context.Background(), bin, "codex://threads/x")
	if err == nil || !strings.Contains(err.Error(), "No application knows how to open URL") {
		t.Fatalf("got %v", err)
	}
}

func TestOpenCommand(t *testing.T) {
	u := "claude://code/continue?session=local_11111111-1111-4111-8111-111111111111"
	if bin, args := openCommand("darwin", u); bin != "/usr/bin/open" || len(args) != 1 || args[0] != u {
		t.Errorf("darwin: %s %v", bin, args)
	}
	// Windows hands the link to rundll32 as one argument, so no shell parses it.
	if bin, args := openCommand("windows", u); bin != "rundll32" || len(args) != 2 || args[0] != "url.dll,FileProtocolHandler" || args[1] != u {
		t.Errorf("windows: %s %v", bin, args)
	}
	for goos, want := range map[string]bool{"darwin": true, "windows": true, "linux": false} {
		if got := app.CanOpen(goos); got != want {
			t.Errorf("CanOpen(%q) = %v, want %v", goos, got, want)
		}
	}
}
