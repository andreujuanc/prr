// Package prcontext discovers and condenses PR-specific context — comments,
// prior reviews, CI status, prior AI reviews — into a small briefing that
// is injected into every review-pass system prompt.
//
// Why this exists:
//
// PR review prompts historically told the model to call gh_pr_comments,
// get_review, gh_pr_checks at request time. Two problems with that:
//  1. Tools the harness exposes are not available to providers like
//     Claude Code, which run their own internal tool loops.
//  2. Tool-based fetching pays a round-trip per phase. Multi-pass review
//     (batch → synthesis → recheck) re-fetches the same data each phase.
//
// This package follows the same pattern as internal/project (the project
// context layer): gather raw inputs, hash them for cache invalidation,
// summarize with the cheap/fast LLM into a small dense briefing, persist
// to state, inject into prompts.
//
// The hash is deliberately conservative — comment body content, review
// body content, label set, CI rollup, and prior-review JSON all
// contribute. If GitHub's `updated_at` glitches or we miss a field, the
// content hash still catches the change.
package prcontext

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/state"
)

// maxBriefOutput caps the LLM output. 400 words ≈ 1600 tokens.
const maxBriefOutput = 1600

// briefSystemPrompt is the summarization instruction; embedded so we
// can edit it as plain markdown.
//
//go:embed prompts/brief.md
var briefSystemPrompt string

// Brief is the final output of a PR-context discovery pass.
type Brief struct {
	// Summary is the rendered briefing, ready for injection into a
	// review prompt's PR Context section. Empty when no inputs were
	// available (no comments, no prior review) AND nothing was worth
	// surfacing — in that case the caller should skip injection.
	Summary string

	// InputHash is a SHA-256 hash of the raw inputs used to generate
	// the summary. Used for cache invalidation.
	InputHash string

	// FromCache indicates whether the result was loaded from cache.
	// When true, Summary will be empty — the caller is expected to use
	// the cached summary from state.GetPRBrief().
	FromCache bool
}

// ghRunner runs a `gh` subcommand and returns stdout. Injectable for
// tests; production code uses runGh.
var ghRunner = runGh

func runGh(args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("gh %s: %w (stderr: %s)", strings.Join(args, " "), err, string(ee.Stderr))
		}
		return "", fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// rawInputs holds the unprocessed data fetched from gh + state.
type rawInputs struct {
	pr            *git.PullRequest
	prJSON        []byte          // gh pr view --json comments,reviews,statusCheckRollup,labels
	priorReview   json.RawMessage // serialized state.AIReview, nil if none
	priorReviewID string          // identifier for hashing (review.Summary[:32]+findings_count or similar)
}

// BuildPRBrief gathers PR-specific context and either returns a cached
// brief (when the input hash matches) or runs the fast LLM to summarize.
//
// Failures are never fatal — on any single error (gh unauthenticated,
// LLM failure), we log and return an empty Brief. The caller proceeds
// with the review using whatever context they already have.
func BuildPRBrief(
	ctx context.Context,
	fastClient ai.Client,
	pr *git.PullRequest,
	reviewState *state.State,
	cachedHash string,
	onProgress func(string),
) (*Brief, error) {
	if onProgress == nil {
		onProgress = func(string) {}
	}
	if pr == nil {
		return &Brief{}, nil
	}

	onProgress("Gathering PR context (comments, reviews, CI)...")

	inputs, gatherErr := gatherPRInputs(pr, reviewState)
	if gatherErr != nil {
		// Don't fail the review — log and proceed without a brief.
		log.Printf("PR brief: gather failed, proceeding without brief: %v", gatherErr)
		return &Brief{}, nil
	}

	hash := hashPRInputs(inputs)

	// Cache hit: caller will use state.GetPRBrief() and skip the LLM call.
	if cachedHash != "" && cachedHash == hash {
		onProgress("PR brief unchanged (cache hit)")
		return &Brief{InputHash: hash, FromCache: true}, nil
	}

	// Need to (re)summarize. If we have no LLM client, return empty —
	// the brief is a quality enhancement, not load-bearing.
	if fastClient == nil {
		return &Brief{InputHash: hash}, nil
	}

	onProgress("Summarizing PR context...")
	summary, err := summarizeWithLLM(ctx, fastClient, inputs)
	if err != nil {
		log.Printf("PR brief: LLM summarization failed, proceeding without brief: %v", err)
		return &Brief{InputHash: hash}, nil
	}

	return &Brief{
		Summary:   wrapBrief(summary),
		InputHash: hash,
	}, nil
}

// gatherPRInputs fetches the raw PR context from gh and reads the
// prior AI review from prr's state. A single gh call covers comments,
// reviews, CI rollup, and labels.
func gatherPRInputs(pr *git.PullRequest, reviewState *state.State) (*rawInputs, error) {
	out, err := ghRunner(
		"pr", "view", fmt.Sprintf("%d", pr.Number),
		"--json", "comments,reviews,statusCheckRollup,labels",
	)
	if err != nil {
		return nil, fmt.Errorf("gh pr view: %w", err)
	}

	inputs := &rawInputs{
		pr:     pr,
		prJSON: []byte(out),
	}

	// Prior AI review (if any) from prr's state cache.
	if reviewState != nil {
		if review := getReview(reviewState); review != nil {
			if data, err := json.Marshal(review); err == nil {
				inputs.priorReview = data
				inputs.priorReviewID = priorReviewID(review)
			}
		}
	}

	return inputs, nil
}

// getReview pulls the cached PR-level AI review out of state. Returns
// nil if none exists.
func getReview(s *state.State) *state.AIReview {
	// state.State is a sync.RWMutex-guarded struct; we access Review
	// through the JSON value directly. There's no public getter yet,
	// so we marshal/unmarshal — cheap, and avoids racing with writers
	// from other goroutines.
	data, err := json.Marshal(s)
	if err != nil {
		return nil
	}
	var probe struct {
		Review *state.AIReview `json:"review"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil
	}
	return probe.Review
}

// priorReviewID produces a short identifier for a prior AI review,
// stable across runs as long as the review content hasn't changed.
// Used to disambiguate "fresh review vs no review" in the input hash.
func priorReviewID(review *state.AIReview) string {
	if review == nil {
		return ""
	}
	h := sha256.New()
	if review.Structured != nil {
		fmt.Fprintf(h, "summary:%s|verdict:%s|findings:%d|missing_tests:%d|questions:%d",
			review.Structured.Summary,
			review.Structured.Verdict,
			len(review.Structured.Findings),
			len(review.Structured.MissingTests),
			len(review.Structured.QuestionsForAuthor))
	}
	h.Write([]byte(review.Summary))
	h.Write([]byte(review.Findings))
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

// hashPRInputs produces a deterministic conservative SHA-256 over
// every input that could affect the summary. Erring toward more
// invalidations: the fast-model summarization is cheap and stale
// briefs hurt review quality more than rebuilds hurt latency.
//
// Inputs covered:
//   - PR Number, Title, Body, BaseRefName, HeadRefName
//   - All comments (id, updated_at, body content)
//   - All reviews (id, submitted_at, state, body content)
//   - All labels (sorted)
//   - statusCheckRollup top-level
//   - Prior AI review (its content hash)
func hashPRInputs(inputs *rawInputs) string {
	if inputs == nil {
		return ""
	}
	h := sha256.New()

	if inputs.pr != nil {
		fmt.Fprintf(h, "pr_number:%d\n", inputs.pr.Number)
		fmt.Fprintf(h, "pr_title:%s\n", inputs.pr.Title)
		fmt.Fprintf(h, "pr_body:%s\n", inputs.pr.Body)
		fmt.Fprintf(h, "base:%s\nhead:%s\n", inputs.pr.BaseRefName, inputs.pr.HeadRefName)
	}

	// Parse the prJSON canonically so map ordering doesn't perturb the
	// hash.
	var parsed struct {
		Comments []struct {
			ID        string `json:"id"`
			UpdatedAt string `json:"updatedAt"`
			Body      string `json:"body"`
		} `json:"comments"`
		Reviews []struct {
			ID          string `json:"id"`
			SubmittedAt string `json:"submittedAt"`
			State       string `json:"state"`
			Body        string `json:"body"`
		} `json:"reviews"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		StatusCheckRollup []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(inputs.prJSON, &parsed); err == nil {
		// Comments — sorted by id for deterministic order.
		sort.Slice(parsed.Comments, func(i, j int) bool { return parsed.Comments[i].ID < parsed.Comments[j].ID })
		for _, c := range parsed.Comments {
			fmt.Fprintf(h, "comment:%s:%s:", c.ID, c.UpdatedAt)
			bodyHash := sha256.Sum256([]byte(c.Body))
			h.Write(bodyHash[:])
			h.Write([]byte{'\n'})
		}
		// Reviews — sorted by id.
		sort.Slice(parsed.Reviews, func(i, j int) bool { return parsed.Reviews[i].ID < parsed.Reviews[j].ID })
		for _, r := range parsed.Reviews {
			fmt.Fprintf(h, "review:%s:%s:%s:", r.ID, r.SubmittedAt, r.State)
			bodyHash := sha256.Sum256([]byte(r.Body))
			h.Write(bodyHash[:])
			h.Write([]byte{'\n'})
		}
		// Labels — sorted by name.
		labelNames := make([]string, len(parsed.Labels))
		for i, l := range parsed.Labels {
			labelNames[i] = l.Name
		}
		sort.Strings(labelNames)
		for _, n := range labelNames {
			fmt.Fprintf(h, "label:%s\n", n)
		}
		// CI status — sorted by check name.
		sort.Slice(parsed.StatusCheckRollup, func(i, j int) bool {
			return parsed.StatusCheckRollup[i].Name < parsed.StatusCheckRollup[j].Name
		})
		for _, s := range parsed.StatusCheckRollup {
			fmt.Fprintf(h, "ci:%s:%s:%s\n", s.Name, s.Status, s.Conclusion)
		}
	}

	if inputs.priorReviewID != "" {
		fmt.Fprintf(h, "prior_review:%s\n", inputs.priorReviewID)
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// wrapBrief returns the LLM output wrapped in a markdown section
// suitable for injection into a review prompt's PR Context block.
func wrapBrief(summary string) string {
	trimmed := strings.TrimSpace(summary)
	if trimmed == "" {
		return ""
	}
	return "## PR Brief\n\n" + trimmed + "\n"
}
