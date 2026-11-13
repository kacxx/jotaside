package hooks

import (
	"context"
	"io"
	"os"
	"regexp"

	"github.com/kacxx/jotaside/internal/app"
	"github.com/kacxx/jotaside/internal/store"
)

// Claude Desktop sets these on the Claude Code process it starts for a
// session, and hooks inherit them. Neither is documented. A terminal `claude`
// started from inside a Desktop session may inherit them too, which is why
// the stored desktop id is only a hint.
const (
	envHostSessionID = "CLAUDE_CODE_HOST_SESSION_ID"
	envEntrypoint    = "CLAUDE_CODE_ENTRYPOINT"
)

var (
	desktopSessionID = regexp.MustCompile(`^local_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	entrypointName   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
)

// ClaudeBlock is the UserPromptSubmit output that blocks and erases a prompt.
type ClaudeBlock struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
	// HookSpecificOutput carries suppressOriginalPrompt: Claude Code ignores
	// it at the top level.
	HookSpecificOutput ClaudeBlockHookSpecific `json:"hookSpecificOutput"`
}

// ClaudeBlockHookSpecific is the hookSpecificOutput of a ClaudeBlock.
type ClaudeBlockHookSpecific struct {
	HookEventName          string `json:"hookEventName"`
	SuppressOriginalPrompt bool   `json:"suppressOriginalPrompt"`
}

// Claude handles a Claude Code UserPromptSubmit hook. Pass-through writes
// nothing (any stdout would be added to the model's context). A block returns
// *BlockExit.
//
// Cursor imports Claude Code hooks and runs them on beforeSubmitPrompt. A
// silent reply there makes Cursor submit the prompt even when `jotaside hook
// cursor` blocked it, so that event is handled exactly as `jotaside hook cursor`
// would handle it.
func Claude(ctx context.Context, r io.Reader, w io.Writer, open app.Opener) error {
	return promptHook{
		event: "UserPromptSubmit",
		alt:   &cursorHook,
		block: func(reason string) any {
			return ClaudeBlock{
				Decision: "block",
				Reason:   reason,
				HookSpecificOutput: ClaudeBlockHookSpecific{
					HookEventName:          "UserPromptSubmit",
					SuppressOriginalPrompt: true,
				},
			}
		},
		parse:     parsePrompt("claude", claudeMeta),
		label:     labelFirstPrompt,
		exitBlock: true,
	}.run(ctx, r, w, open)
}

// sdkEntrypoints are the CLAUDE_CODE_ENTRYPOINT values of headless and Agent
// SDK runs: `claude -p` started from a clean environment (sdk-cli) and the
// TypeScript and Python SDKs. Claude Code treats the three as one group. Such
// runs get no automatic label (#32 D6).
var sdkEntrypoints = map[string]bool{"sdk-cli": true, "sdk-ts": true, "sdk-py": true}

// labelFirstPrompt labels a session with the work item its first prompt names
// (#32): a done "session: <item>" jot, so it doesn't add to the inbox. It
// checks the prompt before anything else, so a prompt without a work item
// never reads the transcript or opens the database. It is silent: any failure
// leaves the session unlabelled and the prompt goes through regardless.
func labelFirstPrompt(ctx context.Context, prompt string, build func() app.CaptureRequest, open app.Opener) {
	defer func() { _ = recover() }()
	item, ok := app.WorkItem(prompt)
	if !ok || sdkEntrypoints[os.Getenv(envEntrypoint)] {
		return
	}
	req := build()
	path, _ := req.Metadata[store.MetaTranscriptPath].(string)
	if req.SessionID == "" || !app.FirstPrompt(path) {
		return
	}
	saveLabel(ctx, req, item, open)
}

// saveLabel opens the database and saves "session: <item>" as req's session
// label, as a done jot, unless the session already has one. Errors are
// dropped: the prompt goes through either way.
func saveLabel(ctx context.Context, req app.CaptureRequest, item string, open app.Opener) {
	svc, err := open()
	if err != nil {
		return
	}
	defer svc.Close()
	req.Text = app.SessionPrefix + " " + item
	_, _, _ = svc.LabelSession(ctx, req)
}

// claudeMeta records the transcript path and, when well-formed, the Claude
// Desktop session id and entrypoint from the environment. Malformed values are
// dropped rather than stored.
func claudeMeta(in PromptInput) map[string]any {
	m := map[string]any{}
	if in.TranscriptPath != "" {
		m[store.MetaTranscriptPath] = in.TranscriptPath
	}
	if v := os.Getenv(envHostSessionID); desktopSessionID.MatchString(v) {
		m[store.MetaDesktopSession] = v
	}
	if v := os.Getenv(envEntrypoint); entrypointName.MatchString(v) {
		m[store.MetaClaudeEntrypoint] = v
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
