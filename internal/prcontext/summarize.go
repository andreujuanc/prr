package prcontext

import (
	"context"
	"fmt"
	"strings"

	"github.com/andreujuanc/prr/internal/ai"
)

// summarizeWithLLM compresses raw PR inputs into a dense ~400-word
// briefing via the fast/cheap LLM. The system prompt lives in
// prompts/brief.md (embedded as briefSystemPrompt).
//
// On any failure the caller should treat the error as non-fatal and
// proceed with an empty brief — the briefing is a quality enhancement
// rather than a load-bearing input.
func summarizeWithLLM(ctx context.Context, client ai.Client, inputs *rawInputs) (string, error) {
	if client == nil {
		return "", fmt.Errorf("nil ai.Client")
	}
	if inputs == nil {
		return "", fmt.Errorf("nil inputs")
	}

	var userMsg strings.Builder

	// PR metadata header.
	if inputs.pr != nil {
		fmt.Fprintf(&userMsg, "PR #%d: %s\n", inputs.pr.Number, inputs.pr.Title)
		if body := strings.TrimSpace(inputs.pr.Body); body != "" {
			fmt.Fprintf(&userMsg, "\nDescription:\n%s\n", body)
		}
		fmt.Fprintf(&userMsg, "\nBase: %s → Head: %s\n", inputs.pr.BaseRefName, inputs.pr.HeadRefName)
	}

	// Raw PR JSON (comments, reviews, CI, labels). Already small relative
	// to the brief's context budget; let the model summarize.
	if len(inputs.prJSON) > 0 {
		userMsg.WriteString("\n=== PR data (comments, reviews, statusCheckRollup, labels) ===\n")
		userMsg.Write(inputs.prJSON)
		userMsg.WriteString("\n")
	}

	// Prior AI review, if any.
	if len(inputs.priorReview) > 0 {
		userMsg.WriteString("\n=== Prior AI review ===\n")
		userMsg.Write(inputs.priorReview)
		userMsg.WriteString("\n")
	}

	messages := []ai.Message{
		{Role: "user", Content: userMsg.String()},
	}

	result, err := client.ChatStream(ctx, briefSystemPrompt, messages, nil)
	if err != nil {
		return "", fmt.Errorf("LLM summarization: %w", err)
	}
	return strings.TrimSpace(result), nil
}
