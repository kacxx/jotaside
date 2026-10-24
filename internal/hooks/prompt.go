package hooks

import (
	"context"
	"fmt"
	"io"

	"github.com/kacxx/jotaside/internal/app"
	"github.com/kacxx/jotaside/internal/capture"
)

// PromptInput is the subset of a UserPromptSubmit payload jot uses. Claude
// Code and Codex share this shape; TurnID and Model are sent only by Codex.
type PromptInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	HookEventName  string `json:"hook_event_name"`
	Prompt         string `json:"prompt"`
	TurnID         string `json:"turn_id"`
	Model          string `json:"model"`
}

// promptHook adapts one agent's prompt-submit hook. run owns the gating
// sequence shared by every agent; the adapter only decodes its payload and
// shapes its replies.
type promptHook struct {
	event string                  // the hook_event_name this hook handles
	pass  any                     // pass-through reply; nil writes nothing
	block func(reason string) any // the agent's blocking reply
	alt   *promptHook             // handles payloads naming its event instead, if set

	// exitBlock makes a block also return *BlockExit, so the host still
	// blocks if it rejects the reply's JSON.
	exitBlock bool

	// parse decodes a payload that fits in memory into its event and prompt,
	// plus a build func that constructs the request to save (Text is filled in
	// by run). build is called only once the prompt is a confirmed jot, so its
	// metadata allocation stays off the path ordinary prompts take. An error
	// means a malformed payload.
	parse func(data []byte) (event, prompt string, build func() app.CaptureRequest, err error)
}

// eventMatches reports whether a payload's hook_event_name is one this hook
// should act on. An empty event (the field wasn't sent) is accepted leniently.
func (h promptHook) eventMatches(event string) bool {
	return event == "" || event == h.event
}

func (h promptHook) run(ctx context.Context, r io.Reader, w io.Writer, open app.Opener) error {
	return h.handle(ctx, readPayload(r), w, open)
}

// handOff reports whether a payload naming event belongs to h.alt.
func (h promptHook) handOff(event string) bool {
	return h.alt != nil && event != "" && event != h.event && event == h.alt.event
}

func (h promptHook) handle(ctx context.Context, p payload, w io.Writer, open app.Opener) error {
	if p.oversized {
		if h.handOff(p.event) {
			return h.alt.handle(ctx, p, w, open)
		}
		if !h.eventMatches(p.event) {
			return h.passThrough(w)
		}
		if _, ok := capture.Match(p.prompt); ok {
			return h.blockWith(w, tooLarge(p.prompt))
		}
		return h.passThrough(w)
	}
	event, prompt, build, err := h.parse(p.data)
	if err != nil {
		return h.passThrough(w)
	}
	if h.handOff(event) {
		return h.alt.handle(ctx, p, w, open)
	}
	if !h.eventMatches(event) {
		return h.passThrough(w)
	}
	text, ok := capture.Match(prompt)
	if !ok {
		return h.passThrough(w)
	}
	return h.blockJot(w, prompt, func() string {
		// Build the request (and its metadata) only now that the prompt is a
		// confirmed jot; an ordinary prompt pays no request or metadata
		// allocation.
		req := build()
		req.Text = text
		return save(ctx, open, req, prompt)
	})
}

// blockJot blocks a recognised jot with the message from reason. A panic in
// reason still blocks it, as NOT saved.
func (h promptHook) blockJot(w io.Writer, prompt string, reason func() string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = h.blockWith(w, notSaved(fmt.Errorf("internal error: %v", r), prompt))
		}
	}()
	return h.blockWith(w, reason())
}

func (h promptHook) blockWith(w io.Writer, reason string) error {
	err := writeJSON(w, h.block(reason))
	if h.exitBlock {
		return &BlockExit{Reason: reason, WriteErr: err}
	}
	return err
}

func (h promptHook) passThrough(w io.Writer) error {
	if h.pass == nil {
		return nil
	}
	return writeJSON(w, h.pass)
}

// parsePrompt returns a promptHook parse function for the UserPromptSubmit
// payload shared by Claude Code and Codex.
func parsePrompt(source string, meta func(in PromptInput) map[string]any) func([]byte) (string, string, func() app.CaptureRequest, error) {
	return func(data []byte) (string, string, func() app.CaptureRequest, error) {
		var in PromptInput
		if err := decodePayload(data, &in); err != nil {
			return "", "", nil, err
		}
		build := func() app.CaptureRequest {
			return app.CaptureRequest{
				Source:    source,
				SessionID: in.SessionID,
				Cwd:       in.Cwd,
				Metadata:  meta(in),
			}
		}
		return in.HookEventName, in.Prompt, build, nil
	}
}
