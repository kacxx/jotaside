package app

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/kacxx/jotaside/internal/gitctx"
)

func TestDBPath(t *testing.T) {
	dir := t.TempDir() // a native absolute path on every OS
	check := func(what, want string) {
		t.Helper()
		got, err := DBPath()
		if err != nil || got != want {
			t.Errorf("%s: got %q (%v), want %q", what, got, err, want)
		}
	}

	custom := filepath.Join(dir, "custom.db")
	t.Setenv("JOT_DB", custom)
	check("JOT_DB", custom)

	t.Setenv("JOT_DB", "rel.db")
	if p, err := DBPath(); err != nil || !filepath.IsAbs(p) || filepath.Base(p) != "rel.db" {
		t.Errorf("relative JOT_DB must be made absolute: %q (%v)", p, err)
	}

	t.Setenv("JOT_DB", "")
	t.Setenv("XDG_DATA_HOME", dir)
	check("XDG_DATA_HOME", filepath.Join(dir, "jot", "jot.db"))

	// The per-OS default, which a relative XDG_DATA_HOME also falls back to.
	var want string
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LOCALAPPDATA", dir)
		want = filepath.Join(dir, "jot", "jot.db")
	case "darwin":
		t.Setenv("HOME", dir)
		want = filepath.Join(dir, "Library", "Application Support", "jot", "jot.db")
	default:
		t.Setenv("HOME", dir)
		want = filepath.Join(dir, ".local", "share", "jot", "jot.db")
	}
	t.Setenv("XDG_DATA_HOME", "")
	check("default", want)
	t.Setenv("XDG_DATA_HOME", "relative")
	check("relative XDG_DATA_HOME", want)
}

func TestBusyTimeout(t *testing.T) {
	t.Setenv("JOT_BUSY_TIMEOUT_MS", "")
	if BusyTimeout() != 2*time.Second {
		t.Error("default")
	}
	t.Setenv("JOT_BUSY_TIMEOUT_MS", "150")
	if BusyTimeout() != 150*time.Millisecond {
		t.Error("override")
	}
	t.Setenv("JOT_BUSY_TIMEOUT_MS", "junk")
	if BusyTimeout() != 2*time.Second {
		t.Error("invalid falls back")
	}
	for _, v := range []string{"10001", "65000", "99999999999999999"} {
		t.Setenv("JOT_BUSY_TIMEOUT_MS", v)
		if got := BusyTimeout(); got != MaxBusyTimeout {
			t.Errorf("%s: got %v, want the %v cap", v, got, MaxBusyTimeout)
		}
	}
	t.Setenv("JOT_BUSY_TIMEOUT_MS", "999999999999999999999") // overflows int64
	if BusyTimeout() != DefaultBusyTimeout {
		t.Error("unparseable falls back")
	}
}

func TestServiceFlow(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "jot.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.git = func(context.Context, string) gitctx.Info {
		return gitctx.Info{Root: "/r", Name: "r", Branch: "b", Commit: "c"}
	}

	e, err := svc.Capture(ctx, CaptureRequest{Text: "  hello  ", Source: "cli", Cwd: "/r",
		Metadata: map[string]any{"x": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != 1 || e.Text != "hello" || e.Branch != "b" || string(e.Metadata) != `{"x":1}` {
		t.Fatalf("capture: %+v", e)
	}
	if _, err := svc.Capture(ctx, CaptureRequest{Text: "  "}); err == nil {
		t.Fatal("empty capture should fail")
	}
	if got, _ := svc.Search(ctx, "HELL", 0); len(got) != 1 {
		t.Fatal("search")
	}
	if _, err := svc.Search(ctx, " ", 0); err == nil {
		t.Fatal("empty search should fail")
	}
	if err := svc.Done(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if in, _ := svc.Inbox(ctx, 10); len(in) != 0 {
		t.Fatal("inbox should be empty")
	}
	if got, _ := svc.Show(ctx, 1); got.Status != "done" {
		t.Fatalf("show: %+v", got)
	}
}

func TestAgeDaysCalendarDays(t *testing.T) {
	ref := time.Date(2026, 10, 3, 0, 10, 0, 0, time.Local)
	for _, tc := range []struct {
		name string
		t    time.Time
		want int
	}{
		{"same day", time.Date(2026, 10, 3, 0, 5, 0, 0, time.Local), 0},
		{"23:50 yesterday is 1d", time.Date(2026, 10, 2, 23, 50, 0, 0, time.Local), 1},
		{"a week ago", time.Date(2026, 9, 26, 12, 0, 0, 0, time.Local), 7},
		{"future clamps to 0", time.Date(2026, 10, 4, 9, 0, 0, 0, time.Local), 0},
	} {
		if got := AgeDays(tc.t, ref); got != tc.want {
			t.Errorf("%s: AgeDays = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestInboxOlder(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "jot.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	base := time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local)
	svc.git = func(context.Context, string) gitctx.Info { return gitctx.Info{} }
	// ids 1..4 are 10, 8, 2 and 0 days old; id 1 is done.
	for _, daysAgo := range []int{10, 8, 2, 0} {
		svc.now = func() time.Time { return base.AddDate(0, 0, -daysAgo) }
		if _, err := svc.Capture(ctx, CaptureRequest{Text: "jot", Source: "cli"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Done(ctx, 1); err != nil {
		t.Fatal(err)
	}
	ids := func(es []Entry) []int64 {
		var out []int64
		for _, e := range es {
			out = append(out, e.ID)
		}
		return out
	}
	es, err := svc.InboxOlder(ctx, 0, 7, base)
	if err != nil || len(es) != 1 || es[0].ID != 2 {
		t.Fatalf("older 7: %v (%v)", ids(es), err)
	}
	if es, _ = svc.InboxOlder(ctx, 0, 2, base); len(es) != 2 || es[0].ID != 3 || es[1].ID != 2 {
		t.Fatalf("older 2 must include exactly 2d: %v", ids(es))
	}
	// The limit applies after the age filter, newest first.
	if es, _ = svc.InboxOlder(ctx, 1, 2, base); len(es) != 1 || es[0].ID != 3 {
		t.Fatalf("older 2 -n 1: %v", ids(es))
	}
	if es, _ = svc.InboxOlder(ctx, 0, 30, base); len(es) != 0 {
		t.Fatalf("older 30: %v", ids(es))
	}
}

func TestCaptureRepeatWindow(t *testing.T) {
	ctx := context.Background()
	svc, err := Open(filepath.Join(t.TempDir(), "jot.db"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.git = func(context.Context, string) gitctx.Info { return gitctx.Info{} }
	now := time.Date(2026, 10, 22, 7, 44, 48, 90_000_000, time.UTC)
	svc.now = func() time.Time { return now }
	capture := func(text, session string, window time.Duration) (Entry, CaptureResult) {
		t.Helper()
		e, r, err := svc.CaptureOnce(ctx, CaptureRequest{Text: text, Source: "claude", SessionID: session, RepeatWindow: window})
		if err != nil {
			t.Fatal(err)
		}
		return e, r
	}

	if e, r := capture("need to get the ArgoCD MCP added", "s1", time.Minute); r != Saved || e.ID != 1 {
		t.Fatalf("first: r=%v id=%d", r, e.ID)
	}
	now = now.Add(35 * time.Millisecond)
	if e, r := capture(" need  to get the ArgoCD MCP added", "s1", time.Minute); r != Repeated || e.ID != 1 {
		t.Fatalf("queued repeat: r=%v id=%d", r, e.ID)
	}
	now = now.Add(61 * time.Second)
	if e, r := capture("need to get the ArgoCD MCP added", "s1", time.Minute); r != Saved || e.ID != 2 {
		t.Fatalf("after the window: r=%v id=%d", r, e.ID)
	}
	// No window (as for `jotaside add`): a repeat is saved.
	if e, r := capture("need to get the ArgoCD MCP added", "s1", 0); r != Saved || e.ID != 3 {
		t.Fatalf("no window: r=%v id=%d", r, e.ID)
	}
}
