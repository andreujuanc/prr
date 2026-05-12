package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/config"
	"github.com/andreujuanc/prr/internal/state"
)

// ── Batch types ─────────────────────────────────────────────────────────

// Batch represents a group of related files to review together.
type Batch struct {
	Label string   // e.g. "internal/ui" or "root"
	Files []string // file paths in this batch
	Diffs string   // concatenated diffs for all files in this batch
}

// BatchFinding is one structured finding from a batch review (new format).
type BatchFinding struct {
	Severity       string `json:"severity,omitempty"`
	Confidence     string `json:"confidence,omitempty"`
	Dimension      string `json:"dimension,omitempty"`
	Title          string `json:"title,omitempty"`
	Line           int    `json:"line,omitempty"`
	Detail         string `json:"detail,omitempty"`
	Suggestion     string `json:"suggestion,omitempty"`
	CWE            string `json:"cwe,omitempty"`
	Exploitability string `json:"exploitability,omitempty"`
	Impact         string `json:"impact,omitempty"`
}

// BatchFindings holds the findings for one file. It deserializes from either
// the new structured array form OR the legacy newline-bullet string form, so
// previously cached results and older models still parse cleanly.
type BatchFindings struct {
	Items []BatchFinding
	Raw   string // populated when the input was the legacy string form
}

// UnmarshalJSON accepts either a JSON array of BatchFinding or a JSON string.
func (bf *BatchFindings) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if trimmed[0] == '[' {
		var items []BatchFinding
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		bf.Items = items
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	bf.Raw = s
	return nil
}

// MarshalJSON emits the structured array form when populated, falling back to
// the legacy string when only Raw is set. Empty values emit "[]".
func (bf BatchFindings) MarshalJSON() ([]byte, error) {
	if len(bf.Items) > 0 {
		return json.Marshal(bf.Items)
	}
	if bf.Raw != "" {
		return json.Marshal(bf.Raw)
	}
	return []byte("[]"), nil
}

// IsEmpty reports whether there are no findings to surface for this file.
func (bf BatchFindings) IsEmpty() bool {
	return len(bf.Items) == 0 && strings.TrimSpace(bf.Raw) == ""
}

// Text renders a human-readable representation for synthesis input. When the
// findings came in as legacy string form, that string is returned verbatim.
func (bf BatchFindings) Text() string {
	if bf.Raw != "" {
		return bf.Raw
	}
	if len(bf.Items) == 0 {
		return ""
	}
	var b strings.Builder
	for i, f := range bf.Items {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- ")
		if f.Severity != "" {
			b.WriteString("[severity: " + f.Severity + "] ")
		}
		if f.Confidence != "" {
			b.WriteString("[confidence: " + f.Confidence + "] ")
		}
		if f.Dimension != "" {
			b.WriteString("[" + f.Dimension + "] ")
		}
		if f.Title != "" {
			b.WriteString(f.Title)
		} else if f.Detail != "" {
			b.WriteString(f.Detail)
		}
		if f.Line > 0 {
			fmt.Fprintf(&b, " (line %d)", f.Line)
		}
		if f.CWE != "" {
			b.WriteString(" [" + f.CWE + "]")
		}
		if f.Exploitability != "" {
			b.WriteString(" [exploitability: " + f.Exploitability + "]")
		}
		if f.Impact != "" {
			b.WriteString(" [impact: " + f.Impact + "]")
		}
		if f.Title != "" && f.Detail != "" {
			b.WriteString("\n  " + f.Detail)
		}
		if f.Suggestion != "" {
			b.WriteString("\n  Suggestion: " + f.Suggestion)
		}
	}
	return b.String()
}

// BatchFileReview is the structured output from reviewing a single file in a batch.
type BatchFileReview struct {
	File     string        `json:"file"`
	Purpose  string        `json:"purpose"`
	Findings BatchFindings `json:"findings"`
}

// BatchMaxChars is the approximate max diff size per batch.
const BatchMaxChars = 20000

// MaxRetries is the number of retry attempts for batch and synthesis calls.
const MaxRetries = 3

// MaxDiffLines is the max number of diff lines included inline.
const MaxDiffLines = 4000

// ── Reporter interface ──────────────────────────────────────────────────

// BatchStatus tracks the state of a batch during review.
type BatchStatus int

const (
	StatusPending BatchStatus = iota
	StatusActive
	StatusDone
	StatusCached
	StatusFailed
)

// BatchInfo describes a single batch for progress reporting.
type BatchInfo struct {
	Label    string
	NumFiles int
}

// Reporter decouples review orchestration from the UI layer.
// The TUI implements this to send Bubble Tea messages;
// the headless CLI implements it to print to stderr.
type Reporter interface {
	AOIProgress(status string, done bool, aoiCount int)
	InitBatches(batches []BatchInfo)
	BatchProgress(batch int, status BatchStatus)
	SynthesisStarted()
	Token(token string)
}

// NopReporter is a Reporter that does nothing.
type NopReporter struct{}

func (NopReporter) AOIProgress(string, bool, int)  {}
func (NopReporter) InitBatches([]BatchInfo)        {}
func (NopReporter) BatchProgress(int, BatchStatus) {}
func (NopReporter) SynthesisStarted()              {}
func (NopReporter) Token(string)                   {}

// WatchdogReporter wraps an existing Reporter and calls `tap` on every
// method invocation. Used to feed an ai.IdleWatch — any progress event
// (token, batch update, AOI progress, synthesis start) counts as
// activity, not just streamed tokens. This catches stalls during long
// tool calls or between phases when nothing is being streamed.
type WatchdogReporter struct {
	Inner Reporter
	Tap   func(string)
}

func (r *WatchdogReporter) AOIProgress(status string, done bool, aoiCount int) {
	if r.Tap != nil {
		r.Tap(status)
	}
	r.Inner.AOIProgress(status, done, aoiCount)
}
func (r *WatchdogReporter) InitBatches(batches []BatchInfo) {
	if r.Tap != nil {
		r.Tap("init batches")
	}
	r.Inner.InitBatches(batches)
}
func (r *WatchdogReporter) BatchProgress(batch int, status BatchStatus) {
	if r.Tap != nil {
		r.Tap("batch progress")
	}
	r.Inner.BatchProgress(batch, status)
}
func (r *WatchdogReporter) SynthesisStarted() {
	if r.Tap != nil {
		r.Tap("synthesis started")
	}
	r.Inner.SynthesisStarted()
}
func (r *WatchdogReporter) Token(token string) {
	if r.Tap != nil {
		r.Tap(token)
	}
	r.Inner.Token(token)
}

// OffsetReporter wraps a Reporter and adds an offset to batch indices.
type OffsetReporter struct {
	RR     Reporter
	Offset int
}

func (o *OffsetReporter) AOIProgress(status string, done bool, aoiCount int) {
	o.RR.AOIProgress(status, done, aoiCount)
}
func (o *OffsetReporter) InitBatches(batches []BatchInfo) {}
func (o *OffsetReporter) BatchProgress(batch int, status BatchStatus) {
	o.RR.BatchProgress(batch+o.Offset, status)
}
func (o *OffsetReporter) SynthesisStarted()  { o.RR.SynthesisStarted() }
func (o *OffsetReporter) Token(token string) { o.RR.Token(token) }

// ── Batch building ──────────────────────────────────────────────────────

// BuildBatches groups changed files into batches by directory,
// respecting the size limit. Files in the same directory are grouped
// together when possible. Large files get their own batch.
func BuildBatches(rawDiffs map[string]string) []Batch {
	dirFiles := make(map[string][]string)
	for p := range rawDiffs {
		if config.ShouldExcludeFromReview(p) {
			continue
		}
		dir := filepath.Dir(p)
		if dir == "." {
			dir = "root"
		}
		dirFiles[dir] = append(dirFiles[dir], p)
	}

	dirs := make([]string, 0, len(dirFiles))
	for d := range dirFiles {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var batches []Batch
	for _, dir := range dirs {
		files := dirFiles[dir]
		sort.Strings(files)

		var curFiles []string
		var curDiff strings.Builder

		for _, f := range files {
			diff := rawDiffs[f]
			entry := fmt.Sprintf("=== %s ===\n%s\n\n", f, diff)

			if curDiff.Len() > 0 && curDiff.Len()+len(entry) > BatchMaxChars {
				batches = append(batches, Batch{
					Label: dir,
					Files: curFiles,
					Diffs: curDiff.String(),
				})
				curFiles = nil
				curDiff.Reset()
			}

			curDiff.WriteString(entry)
			curFiles = append(curFiles, f)
		}

		if len(curFiles) > 0 {
			batches = append(batches, Batch{
				Label: dir,
				Files: curFiles,
				Diffs: curDiff.String(),
			})
		}
	}

	return batches
}

// ── Diff capping ────────────────────────────────────────────────────────

// CapDiff truncates a diff to MaxDiffLines and appends a tool-use hint.
func CapDiff(diff string, files []string) string {
	lines := strings.Split(diff, "\n")
	if len(lines) <= MaxDiffLines {
		return diff
	}
	capped := strings.Join(lines[:MaxDiffLines], "\n")
	pathList := strings.Join(files, " ")
	return capped + fmt.Sprintf(ai.HintDiffTruncated, MaxDiffLines, len(lines)-MaxDiffLines, pathList)
}

// ── Batch prompt building ───────────────────────────────────────────────

// BuildBatchSystemPrompt constructs the system prompt for a batch review.
func BuildBatchSystemPrompt(prMeta, customInstructions string) string {
	systemPrompt := ai.ReviewBatchPrompt + "\n\n## PR Context\n" + prMeta
	if customInstructions != "" {
		systemPrompt += "\n\n## Project-Specific Instructions\n\n" + customInstructions
	}
	return systemPrompt
}

// BuildBatchMessages constructs the user message for a batch review.
func BuildBatchMessages(batch Batch) []ai.Message {
	return []ai.Message{
		{Role: "user", Content: fmt.Sprintf(
			"Review these %d file(s): %s\n\n%s",
			len(batch.Files),
			strings.Join(batch.Files, ", "),
			CapDiff(batch.Diffs, batch.Files),
		)},
	}
}

// ── Parsing ─────────────────────────────────────────────────────────────

// ParseBatchResult parses the JSON array from a batch review response.
// Handles markdown code fences that AI models commonly wrap around JSON.
func ParseBatchResult(raw string) []BatchFileReview {
	s := ai.StripMarkdownFences(raw)

	var results []BatchFileReview
	if err := json.Unmarshal([]byte(s), &results); err != nil {
		log.Printf("Warning: failed to parse batch JSON: %v", err)
		return nil
	}
	return results
}

// ── Retry logic ─────────────────────────────────────────────────────────

// ReviewBatchWithRetry calls ChatStream for a batch and retries up to MaxRetries
// times if the result is empty or unparseable.
func ReviewBatchWithRetry(
	ctx context.Context,
	client ai.Client,
	systemPrompt string,
	batch Batch,
	onToken func(string),
) (string, error) {
	if onToken == nil {
		onToken = func(string) {}
	}

	var lastResult string
	for attempt := 1; attempt <= MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		result, err := client.ChatStream(ctx, systemPrompt, BuildBatchMessages(batch), onToken)
		if err != nil {
			return "", err
		}

		lastResult = result
		trimmed := strings.TrimSpace(result)

		if trimmed != "" && ParseBatchResult(trimmed) != nil {
			return result, nil
		}

		if attempt < MaxRetries {
			reason := "empty response"
			if trimmed != "" {
				reason = "unparseable response"
			}
			log.Printf("Batch %q attempt %d/%d: %s, retrying...", batch.Label, attempt, MaxRetries, reason)
		}
	}

	log.Printf("Batch %q: exhausted %d retries, using last result as fallback", batch.Label, MaxRetries)
	return lastResult, nil
}

// SynthesisWithRetry calls ChatStream for synthesis and retries up to MaxRetries
// times if the result is empty.
func SynthesisWithRetry(
	ctx context.Context,
	client ai.Client,
	systemPrompt string,
	messages []ai.Message,
	onToken func(string),
) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= MaxRetries; attempt++ {
		// Caller cancellation (user Esc or idle watchdog) is terminal —
		// don't retry. The cache will be reused on a fresh run.
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		result, err := client.ChatStream(ctx, systemPrompt, messages, onToken)
		if err != nil {
			// Transient errors (rate-limit, 5xx, network blip) are
			// worth one or two more shots — the per-phase work upstream
			// is already cached, so losing synthesis to a flap and then
			// restarting from scratch is wasteful.
			if isTransientClientError(err) && attempt < MaxRetries {
				backoff := transientBackoff(attempt)
				log.Printf("Synthesis attempt %d/%d: transient error (%v), retrying in %v", attempt, MaxRetries, err, backoff)
				lastErr = err
				select {
				case <-time.After(backoff):
					continue
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			return "", err
		}

		summary := strings.TrimSpace(result)
		if summary != "" {
			return summary, nil
		}

		lastErr = fmt.Errorf("synthesis returned empty response")
		if attempt < MaxRetries {
			log.Printf("Synthesis attempt %d/%d: empty response, retrying...", attempt, MaxRetries)
		}
	}

	return "", lastErr
}

// transientStatusCodeRe matches HTTP status codes that are worth
// retrying when they appear as standalone tokens in an error message.
// Word-bounded so we don't false-positive on "exceeded 500 tokens" or
// "duration 5000ms".
var transientStatusCodeRe = regexp.MustCompile(`\b(429|500|502|503|504)\b`)

// transientPhraseRe matches phrase-level signals of transient failure.
// All matches are lowercase; the haystack is lowercased before checking.
// EOF is bounded on both sides — "eof " or " eof" or " eof," — so it
// doesn't match inside words like "endpointoflist".
var transientPhraseRe = regexp.MustCompile(
	`rate limit|timeout|temporary failure|connection reset|\beof\b`,
)

// isTransientClientError reports whether the underlying AI client error
// is the kind that may succeed on retry (rate limit, server error,
// transient network issue). User-initiated cancellation and watchdog
// idle-cancel are NOT considered transient — the caller already decided
// to stop.
func isTransientClientError(err error) bool {
	if err == nil {
		return false
	}
	// User cancel / context deadline / watchdog idle: terminal.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// io.EOF wrapped through fmt.Errorf — common when an upstream
	// stream closes unexpectedly. Bare io.EOF check first (cheap), then
	// fall through to text matching for wrapped-string variants.
	if errors.Is(err, io.EOF) {
		return true
	}
	s := strings.ToLower(err.Error())
	if transientStatusCodeRe.MatchString(s) {
		return true
	}
	if transientPhraseRe.MatchString(s) {
		return true
	}
	return false
}

// transientBackoff returns a quadratic backoff suitable for retry
// attempt n (1-indexed): 1s, 4s, 9s, …
func transientBackoff(attempt int) time.Duration {
	return time.Duration(attempt*attempt) * time.Second
}

// ── Batch persistence ───────────────────────────────────────────────────

// PersistBatchFindings parses a batch result and saves per-file purpose+findings
// to state. Returns the parsed reviews and a map of file->findings.
func PersistBatchFindings(reviewState *state.State, batch Batch, rawResult string) ([]BatchFileReview, map[string]string) {
	parsed := ParseBatchResult(rawResult)
	fileFindings := make(map[string]string)

	if parsed == nil {
		log.Printf("Warning: batch %q returned unparseable result, using raw fallback", batch.Label)
		if reviewState != nil {
			for _, f := range batch.Files {
				reviewState.SetBatchFindings(f, "unknown (parse failed)", rawResult)
			}
			if len(batch.Files) > 0 {
				fileFindings[batch.Files[0]] = rawResult
			}
		}
	} else {
		batchFiles := make(map[string]bool, len(batch.Files))
		for _, f := range batch.Files {
			batchFiles[f] = true
		}

		matchedFiles := make(map[string]bool)
		for _, entry := range parsed {
			if !batchFiles[entry.File] {
				log.Printf("Warning: AI returned file %q not in batch %v", entry.File, batch.Files)
				continue
			}
			matchedFiles[entry.File] = true
			findingsText := entry.Findings.Text()
			if reviewState != nil {
				purpose := entry.Purpose
				if purpose == "" {
					purpose = "reviewed"
				}
				reviewState.SetBatchFindings(entry.File, purpose, findingsText)
			}
			if !entry.Findings.IsEmpty() {
				fileFindings[entry.File] = findingsText
			}
		}

		if len(matchedFiles) < len(batch.Files) {
			log.Printf("Warning: batch %q has %d files but AI only returned %d matching entries",
				batch.Label, len(batch.Files), len(matchedFiles))
		}

		if reviewState != nil {
			for _, f := range batch.Files {
				purpose, _ := reviewState.GetBatchFindings(f)
				if purpose == "" {
					reviewState.SetBatchFindings(f, "reviewed (no details)", "")
				}
			}
		}
	}

	if reviewState != nil {
		if err := state.Save(reviewState); err != nil {
			log.Printf("Warning: failed to persist batch findings: %v", err)
		}
	}

	return parsed, fileFindings
}

// ── Batch caching ───────────────────────────────────────────────────────

// IsBatchCached checks if all files in a batch have cached findings.
func IsBatchCached(batch Batch, reviewState *state.State) bool {
	if reviewState == nil {
		return false
	}
	if !reviewState.HasCachedBatch(batch.Files) {
		for _, f := range batch.Files {
			purpose, _ := reviewState.GetBatchFindings(f)
			if purpose == "" {
				log.Printf("Cache miss: file %q (purpose empty)", f)
				break
			}
		}
		return false
	}
	return true
}

// CollectCachedFindings reassembles per-file findings from cache.
func CollectCachedFindings(batch Batch, reviewState *state.State) (string, map[string]string) {
	return reviewState.CollectCachedFindings(batch.Files)
}

// ── Diff stats ──────────────────────────────────────────────────────────

// CountDiffStats counts added and removed lines in a unified diff.
func CountDiffStats(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	return
}

// ── Synthesis ───────────────────────────────────────────────────────────

// SynthesisResult holds the output of the synthesis phase.
type SynthesisResult struct {
	Review     *state.AIReview
	Structured *state.ReviewOutput
}

// RunSynthesis runs Phase 2: synthesize all findings into a final review.
func RunSynthesis(
	ctx context.Context,
	client ai.Client,
	prMeta string,
	rawDiffs map[string]string,
	customInstructions string,
	allFindings string,
	fileFindings map[string]string,
	rr Reporter,
) (*SynthesisResult, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if rr != nil {
		rr.SynthesisStarted()
	}

	// Build file listing
	var fileListing strings.Builder
	paths := make([]string, 0, len(rawDiffs))
	for fp := range rawDiffs {
		paths = append(paths, fp)
	}
	sort.Strings(paths)
	fileListing.WriteString(fmt.Sprintf("Files changed (%d):\n", len(paths)))
	for _, fp := range paths {
		diff := rawDiffs[fp]
		added, removed := CountDiffStats(diff)
		fileListing.WriteString(fmt.Sprintf("  %-50s +%-4d -%d\n", fp, added, removed))
	}

	synthesisSystem := ai.ReviewSynthesisPrompt + "\n\n" +
		"## PR Metadata\n" + prMeta + "\n" +
		"## Changed Files\n" + fileListing.String() + "\n" +
		"## Per-batch Findings\n\n" + allFindings
	if customInstructions != "" {
		synthesisSystem += "\n\n## Project-Specific Instructions\n\n" + customInstructions
	}

	synthesisMessages := []ai.Message{
		{Role: "user", Content: "Synthesize the per-file findings into a final PR review. Use tools to verify any findings you are uncertain about. Return ONLY the JSON review object."},
	}

	onToken := func(string) {}
	if rr != nil {
		onToken = rr.Token
	}

	summary, err := SynthesisWithRetry(ctx, client, synthesisSystem, synthesisMessages, onToken)
	if err != nil {
		// Surface idle-cancellation distinctly so the user understands
		// what happened and that re-running will resume from the cached
		// batch findings rather than redoing the whole pipeline.
		if cause := context.Cause(ctx); errors.Is(cause, ai.ErrIdle) {
			return nil, fmt.Errorf("synthesis stalled (no AI activity for the idle window) — re-run will resume from cached batch findings: %w", cause)
		}
		return nil, fmt.Errorf("synthesis: %w", err)
	}

	// Parse structured output
	structured := ai.ParseReviewOutput(summary)

	if structured == nil {
		// Log the raw summary for debugging
		if len(summary) > 500 {
			log.Printf("Synthesis: parse failed. Summary length=%d, first 500 chars: %s", len(summary), summary[:500])
		} else {
			log.Printf("Synthesis: parse failed. Summary: %s", summary)
		}

		// Retry with correction
		log.Printf("Synthesis: initial JSON parse failed, retrying with correction prompt")

		// Detect whether the response looks like truncated JSON.
		trimmed := strings.TrimSpace(summary)
		isTruncated := (strings.Contains(trimmed, `"summary"`) || strings.Contains(trimmed, `"findings"`)) &&
			!strings.HasSuffix(trimmed, "}")

		correctionHint := "Your response was not valid JSON. Please return ONLY a valid JSON object matching the schema specified in the system prompt. No markdown, no prose — just the raw JSON object starting with { and ending with }."
		if isTruncated {
			correctionHint = "Your response was truncated — it appears the output was cut off before the JSON was complete. Please return the SAME review but be MORE CONCISE in your detail/suggestion fields so the full JSON fits. Return ONLY the JSON object, no markdown fences, no prose."
		}

		correctionMessages := []ai.Message{
			{Role: "user", Content: "Synthesize the per-file findings into a final PR review. Return ONLY the JSON review object."},
			{Role: "assistant", Content: summary},
			{Role: "user", Content: correctionHint},
		}

		corrected, corrErr := SynthesisWithRetry(ctx, client, synthesisSystem, correctionMessages, onToken)
		if corrErr == nil {
			if parsed := ai.ParseReviewOutput(corrected); parsed != nil {
				structured = parsed
				summary = corrected
			}
		}
	}

	return &SynthesisResult{
		Review: &state.AIReview{
			Summary:  summary,
			Findings: allFindings,
		},
		Structured: structured,
	}, nil
}

// ── Fallback batch runner ───────────────────────────────────────────────

// RunBatchesOnly reviews batches sequentially or in parallel (no synthesis).
// Returns findings text, per-file findings map, and any error.
func RunBatchesOnly(
	ctx context.Context,
	client ai.Client,
	prMeta string,
	rawDiffs map[string]string,
	customInstructions string,
	reviewState *state.State,
	batches []Batch,
	rr Reporter,
) (string, map[string]string, error) {
	var allFindings strings.Builder
	allFileFindings := make(map[string]string)

	systemPrompt := BuildBatchSystemPrompt(prMeta, customInstructions)

	if len(batches) > 1 {
		// Parallel
		type result struct {
			index  int
			text   string
			err    error
			cached bool
		}
		results := make([]result, len(batches))
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 5)

		for i, batch := range batches {
			wg.Add(1)
			go func(idx int, b Batch) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				if ctx.Err() != nil {
					results[idx] = result{index: idx, err: ctx.Err()}
					rr.BatchProgress(idx, StatusFailed)
					return
				}

				if IsBatchCached(b, reviewState) {
					cached, cachedFF := CollectCachedFindings(b, reviewState)
					results[idx] = result{index: idx, text: cached, cached: true}
					mu.Lock()
					for f, findings := range cachedFF {
						allFileFindings[f] = findings
					}
					mu.Unlock()
					rr.BatchProgress(idx, StatusCached)
					return
				}

				rr.BatchProgress(idx, StatusActive)
				res, err := ReviewBatchWithRetry(ctx, client, systemPrompt, b, nil)
				results[idx] = result{index: idx, text: res, err: err}

				if err != nil {
					rr.BatchProgress(idx, StatusFailed)
				} else {
					rr.BatchProgress(idx, StatusDone)
				}
			}(i, batch)
		}
		wg.Wait()

		for i, res := range results {
			if res.err != nil {
				return "", nil, fmt.Errorf("batch %d/%d (%s): %w", i+1, len(batches), batches[i].Label, res.err)
			}

			allFindings.WriteString(fmt.Sprintf("### Batch %d: %s\n", i+1, batches[i].Label))
			allFindings.WriteString(fmt.Sprintf("Files: %s\n\n", strings.Join(batches[i].Files, ", ")))

			if !res.cached {
				parsed, batchFF := PersistBatchFindings(reviewState, batches[i], res.text)
				for f, findings := range batchFF {
					allFileFindings[f] = findings
				}
				if parsed != nil {
					for _, entry := range parsed {
						if !entry.Findings.IsEmpty() {
							allFindings.WriteString(fmt.Sprintf("#### %s\nPurpose: %s\n%s\n\n",
								entry.File, entry.Purpose, entry.Findings.Text()))
						}
					}
				} else {
					allFindings.WriteString(res.text)
				}
			} else {
				allFindings.WriteString(res.text)
			}
			allFindings.WriteString("\n\n---\n\n")
		}
	} else {
		// Sequential (single or zero batches)
		for i, batch := range batches {
			if ctx.Err() != nil {
				return "", nil, ctx.Err()
			}

			if IsBatchCached(batch, reviewState) {
				rr.BatchProgress(i, StatusCached)
				cached, cachedFF := CollectCachedFindings(batch, reviewState)
				allFindings.WriteString(fmt.Sprintf("### Batch %d: %s\n", i+1, batch.Label))
				allFindings.WriteString(fmt.Sprintf("Files: %s\n\n", strings.Join(batch.Files, ", ")))
				allFindings.WriteString(cached)
				allFindings.WriteString("\n\n---\n\n")
				for f, findings := range cachedFF {
					allFileFindings[f] = findings
				}
				continue
			}

			rr.BatchProgress(i, StatusActive)
			result, err := ReviewBatchWithRetry(ctx, client, systemPrompt, batch, nil)
			if err != nil {
				rr.BatchProgress(i, StatusFailed)
				return "", nil, fmt.Errorf("batch %d/%d (%s): %w", i+1, len(batches), batch.Label, err)
			}

			rr.BatchProgress(i, StatusDone)
			parsed, batchFF := PersistBatchFindings(reviewState, batch, result)
			for f, findings := range batchFF {
				allFileFindings[f] = findings
			}

			allFindings.WriteString(fmt.Sprintf("### Batch %d: %s\n", i+1, batch.Label))
			allFindings.WriteString(fmt.Sprintf("Files: %s\n\n", strings.Join(batch.Files, ", ")))
			if parsed != nil {
				for _, entry := range parsed {
					if !entry.Findings.IsEmpty() {
						allFindings.WriteString(fmt.Sprintf("#### %s\nPurpose: %s\n%s\n\n",
							entry.File, entry.Purpose, entry.Findings.Text()))
					}
				}
			} else {
				allFindings.WriteString(result)
			}
			allFindings.WriteString("\n\n---\n\n")
		}
	}

	return allFindings.String(), allFileFindings, nil
}

// ── Deep findings helper ────────────────────────────────────────────────

// AppendDeepFindings writes deep findings into a strings.Builder for synthesis
// and indexes them by file in the fileFindings map.
func AppendDeepFindings(b *strings.Builder, fileFindings map[string]string, findings []state.DeepFinding) {
	for _, f := range findings {
		b.WriteString(fmt.Sprintf("### %s: %s\n", f.Severity, f.Title))
		b.WriteString(fmt.Sprintf("**File:** %s:%s\n", f.File, f.Lines))
		b.WriteString(fmt.Sprintf("**Category:** %s/%s\n", f.Category, f.Subcategory))
		b.WriteString(fmt.Sprintf("**Description:** %s\n", f.Description))
		if f.Trigger != "" {
			b.WriteString(fmt.Sprintf("**Trigger:** %s\n", f.Trigger))
		}
		if f.Suggestion != "" {
			b.WriteString(fmt.Sprintf("**Suggestion:** %s\n", f.Suggestion))
		}
		b.WriteString("\n---\n\n")

		entry := fmt.Sprintf("[%s] %s: %s", f.Severity, f.Title, f.Description)
		if existing, ok := fileFindings[f.File]; ok {
			fileFindings[f.File] = existing + "\n\n" + entry
		} else {
			fileFindings[f.File] = entry
		}
	}
}
