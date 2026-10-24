package hooks

import (
	"context"
	"io"

	"github.com/kacxx/jotaside/internal/app"
	"github.com/kacxx/jotaside/internal/store"
)

// CodexBlock is the Codex UserPromptSubmit output that blocks a prompt; the
// reason is shown to the user. Codex documents no suppressOriginalPrompt.
type CodexBlock struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// Codex handles a Codex UserPromptSubmit hook. Pass-through writes nothing:
// Codex adds plain stdout to the model's context as developer context. A block
// returns *BlockExit: Codex treats JSON with an unknown or wrong-typed field
// as a failed hook and sends the prompt on, while exit 2 blocks and shows
// stderr in place of the JSON reason.
func Codex(ctx context.Context, r io.Reader, w io.Writer, open app.Opener) error {
	return promptHook{
		event:     "UserPromptSubmit",
		exitBlock: true,
		block: func(reason string) any {
			return CodexBlock{Decision: "block", Reason: reason}
		},
		parse: parsePrompt("codex", func(in PromptInput) map[string]any {
			m := map[string]any{}
			// Nothing reads a Codex transcript yet; the path is kept so that a
			// reader can be added later for jots already captured.
			if in.TranscriptPath != "" {
				m[store.MetaTranscriptPath] = in.TranscriptPath
			}
			if in.TurnID != "" {
				m["turn_id"] = in.TurnID
			}
			if in.Model != "" {
				m["model"] = in.Model
			}
			if len(m) == 0 {
				return nil
			}
			return m
		}),
	}.run(ctx, r, w, open)
}
