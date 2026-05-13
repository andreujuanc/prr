// Package classify groups changed files by their architectural role
// (handler, repository, model, test, …) so each file's AOI pre-scan
// can be narrowed to the dimensions that actually apply. A test file
// doesn't need a cryptography review; a handler does need an
// input-validation pass.
//
// Shared by `prr audit` (whole-repo) and `prr review` (PR diffs) —
// both feed the result into security.ScanAreasOfInterestClassified.
package classify

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/andreujuanc/prr/internal/ai"
)

// FileType is the architectural role of a source file.
type FileType string

const (
	FileTypeTest           FileType = "test"
	FileTypeHandler        FileType = "handler"
	FileTypeRepository     FileType = "repository"
	FileTypeSQL            FileType = "sql"
	FileTypeModel          FileType = "model"
	FileTypeClient         FileType = "client"
	FileTypeWorker         FileType = "worker"
	FileTypeBusinessLogic  FileType = "business-logic"
	FileTypeInfrastructure FileType = "infrastructure"
	FileTypeUnknown        FileType = "unknown"
)

// AllFileTypes lists valid classification types for prompt construction.
var AllFileTypes = []FileType{
	FileTypeTest,
	FileTypeHandler,
	FileTypeRepository,
	FileTypeSQL,
	FileTypeModel,
	FileTypeClient,
	FileTypeWorker,
	FileTypeBusinessLogic,
	FileTypeInfrastructure,
	FileTypeUnknown,
}

// DimensionsForType returns the dimension slugs to include in the AOI
// prompt for the given file type. Unknown types get all dimensions.
func DimensionsForType(ft FileType) []string {
	switch ft {
	case FileTypeTest:
		return []string{"testing", "correctness"}
	case FileTypeHandler:
		return []string{"input-validation", "authentication", "authorization", "web-security", "error-handling", "api-design", "performance", "observability", "test-coverage"}
	case FileTypeRepository:
		return []string{"data-integrity", "input-validation", "error-handling", "resource-management", "concurrency", "observability", "test-coverage"}
	case FileTypeSQL:
		// Raw SQL files (migrations, query files, schema definitions)
		// have a very different review surface from application-layer
		// repository code. The dominant risks are schema integrity,
		// migration safety/reversibility, query correctness, lock
		// duration on prod-scale tables, and missing indices —
		// NOT connection handling or error wrapping (which only
		// exist in the calling code).
		return []string{"data-integrity", "correctness", "performance", "design", "observability"}
	case FileTypeModel:
		return []string{"api-design", "input-validation", "data-integrity", "correctness", "test-coverage"}
	case FileTypeClient:
		return []string{"external-io", "error-handling", "resource-management", "input-validation", "observability", "test-coverage"}
	case FileTypeWorker:
		return []string{"concurrency", "error-handling", "resource-management", "external-io", "correctness", "observability", "test-coverage"}
	case FileTypeBusinessLogic:
		return []string{"correctness", "data-integrity", "error-handling", "design", "financial", "concurrency", "observability", "test-coverage"}
	case FileTypeInfrastructure:
		return []string{"configuration", "error-handling", "resource-management", "web-security", "observability", "test-coverage"}
	default:
		return ai.AllDimensionSlugs()
	}
}

// File is the classifier input: a path and (a snippet of) its content.
// Audit reads full file content from disk via CollectFiles; review
// reads the head-of-branch version of each diffed file. Either way
// only the first ~50 lines are sent to the model — enough for imports
// and top-level declarations.
type File struct {
	Path    string
	Content string
}

// FileClassification holds a file path and its LLM-determined type.
type FileClassification struct {
	File string   `json:"file"`
	Type FileType `json:"type"`
}

// batchMaxFiles caps how many files we send per classification call.
const batchMaxFiles = 50

// defaultMaxConcurrency is the default cap on parallel classification
// calls. SetMaxConcurrency overrides this for the lifetime of the process.
const defaultMaxConcurrency = 5

var maxConcurrency = defaultMaxConcurrency

// SetMaxConcurrency sets the max number of classification batches run
// in parallel. Values <= 0 reset to the default. Not safe to call
// concurrently with classification in flight; intended to be called
// once at startup.
func SetMaxConcurrency(n int) {
	if n <= 0 {
		maxConcurrency = defaultMaxConcurrency
		return
	}
	maxConcurrency = n
}

//go:embed prompts/classify.md
var classifyPrompt string

// Classify runs the LLM classifier on the given files using the cheap
// model. Returns a map of file path → FileType.
//
// cachedTypes maps file paths to previously cached classifications.
// Files with cached results are skipped. Pass nil to classify everything.
//
// Returns partial results plus errors.Join of any batch errors, so the
// caller can decide whether to fail or proceed with unknowns.
func Classify(
	ctx context.Context,
	client ai.Client,
	files []File,
	cachedTypes map[string]FileType,
	onProgress func(status string),
) (map[string]FileType, error) {
	result := make(map[string]FileType, len(files))

	// Separate cached vs uncached.
	var uncached []File
	for _, f := range files {
		if ft, ok := cachedTypes[f.Path]; ok {
			result[f.Path] = ft
			continue
		}
		uncached = append(uncached, f)
	}

	if len(uncached) == 0 {
		if onProgress != nil {
			onProgress(fmt.Sprintf("all %d file classifications from cache", len(files)))
		}
		return result, nil
	}

	if onProgress != nil {
		onProgress(fmt.Sprintf("classifying %d file(s) (%d cached)...", len(uncached), len(files)-len(uncached)))
	}

	batches := buildBatches(uncached)

	type batchResult struct {
		index   int
		results []FileClassification
		err     error
	}

	resultsCh := make(chan batchResult, len(batches))
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup

	for i, batch := range batches {
		wg.Add(1)
		go func(i int, batch []File) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				resultsCh <- batchResult{index: i, err: ctx.Err()}
				return
			}

			classifications, err := classifyBatch(ctx, client, batch)
			resultsCh <- batchResult{index: i, results: classifications, err: err}
		}(i, batch)
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	var batchErrs []error
	for br := range resultsCh {
		if br.err != nil {
			log.Printf("Classification batch %d failed: %v", br.index+1, br.err)
			batchErrs = append(batchErrs, fmt.Errorf("batch %d: %w", br.index+1, br.err))
			continue
		}
		for _, fc := range br.results {
			result[fc.File] = fc.Type
		}
	}

	// Files not classified (errors or missing from response) get "unknown"
	// so downstream code can safely look up every input.
	for _, f := range uncached {
		if _, ok := result[f.Path]; !ok {
			result[f.Path] = FileTypeUnknown
		}
	}

	if onProgress != nil {
		onProgress(fmt.Sprintf("classified %d file(s)", len(uncached)))
	}

	return result, errors.Join(batchErrs...)
}

// Classification windowing.
//
// Small files (≤ classifyWindowThreshold lines) are sent in full —
// the model gets every signal there is.
//
// Large files are sampled as TOP + MIDDLE rather than just TOP:
// imports (top) give strong signal but miss the "what does this
// file actually do" content that often lives in the middle.
// Sending the middle window catches handler bodies, business logic,
// query patterns, registration calls, etc. that imports alone
// don't reveal.
//
// The two windows never overlap — for a 110-line file, top = 1-50
// and middle would natively center at 30-80, so we shift middle
// down to start at line 51. For a 1000-line file, top = 1-50 and
// middle = 476-525 (centered on total/2 = 500).
const (
	classifyTopWindow       = 50
	classifyMidWindow       = 50
	classifyWindowThreshold = 100
)

// windowForClassify returns the slice of file content sent to the
// classifier. See the windowing constants above for the strategy.
func windowForClassify(content string) string {
	lines := strings.Split(content, "\n")
	if len(lines) <= classifyWindowThreshold {
		return content
	}

	total := len(lines)
	top := lines[:classifyTopWindow]

	// Center the middle window on total/2, but never let it overlap
	// the top window.
	midStart := total/2 - classifyMidWindow/2
	if midStart < classifyTopWindow {
		midStart = classifyTopWindow
	}
	midEnd := midStart + classifyMidWindow
	if midEnd > total {
		midEnd = total
	}
	middle := lines[midStart:midEnd]

	omitted := midStart - classifyTopWindow

	var b strings.Builder
	b.WriteString(strings.Join(top, "\n"))
	b.WriteString("\n")
	if omitted > 0 {
		// Marker only when there's a real gap. For a 110-line file
		// (top 1-50 + middle 51-100), top and middle are contiguous;
		// inserting a marker would confuse the model into thinking
		// content was elided when it wasn't.
		fmt.Fprintf(&b, "... [%d lines omitted] ...\n", omitted)
	}
	b.WriteString(strings.Join(middle, "\n"))
	// Anything after midEnd is intentionally dropped — for classification
	// the role signal is already strong from top + middle. Sending the
	// tail too would double the per-file cost without changing accuracy.
	return b.String()
}

// buildBatches splits files into batches of batchMaxFiles.
func buildBatches(files []File) [][]File {
	var batches [][]File
	for i := 0; i < len(files); i += batchMaxFiles {
		end := i + batchMaxFiles
		if end > len(files) {
			end = len(files)
		}
		batches = append(batches, files[i:end])
	}
	return batches
}

// classifyBatch sends a batch of files to the LLM for classification.
func classifyBatch(ctx context.Context, client ai.Client, files []File) ([]FileClassification, error) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Classify these %d file(s):\n\n", len(files)))

	for _, f := range files {
		sb.WriteString(fmt.Sprintf("=== %s ===\n", f.Path))
		sb.WriteString(windowForClassify(f.Content))
		sb.WriteString("\n\n")
	}

	messages := []ai.Message{
		{Role: "user", Content: sb.String()},
	}

	raw, err := client.ChatStream(ctx, classifyPrompt, messages, nil)
	if err != nil {
		return nil, fmt.Errorf("classify LLM call: %w", err)
	}

	return parseResult(raw)
}

// parseResult parses the LLM classification response.
func parseResult(raw string) ([]FileClassification, error) {
	s := strings.TrimSpace(raw)

	// Strip markdown fences
	if strings.HasPrefix(s, "```") {
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		if idx := strings.LastIndex(s, "```"); idx != -1 {
			s = s[:idx]
		}
		s = strings.TrimSpace(s)
	}

	if !strings.HasPrefix(s, "[") {
		start := strings.Index(s, "[")
		if start == -1 {
			return nil, fmt.Errorf("no JSON array found in classification response")
		}
		s = s[start:]
	}

	var results []FileClassification
	if err := json.Unmarshal([]byte(s), &results); err != nil {
		return nil, fmt.Errorf("parse classification JSON: %w", err)
	}

	// Validate types — replace invalid with unknown.
	for i := range results {
		if !isValidFileType(results[i].Type) {
			results[i].Type = FileTypeUnknown
		}
	}

	return results, nil
}

func isValidFileType(ft FileType) bool {
	for _, valid := range AllFileTypes {
		if ft == valid {
			return true
		}
	}
	return false
}
