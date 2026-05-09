package audit

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/andreujuanc/prr/internal/ai"
)

// FileType represents the classification of a source file.
type FileType string

const (
	FileTypeTest           FileType = "test"
	FileTypeHandler        FileType = "handler"
	FileTypeRepository     FileType = "repository"
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

// FileClassification holds a file path and its LLM-determined type.
type FileClassification struct {
	File string   `json:"file"`
	Type FileType `json:"type"`
}

// classifyBatchMaxFiles caps how many files we send per classification call.
const classifyBatchMaxFiles = 50

// defaultClassifyMaxConcurrency is the default cap on parallel classification
// calls. SetClassifyConcurrency overrides this for the lifetime of the process.
const defaultClassifyMaxConcurrency = 5

var classifyMaxConcurrency = defaultClassifyMaxConcurrency

// SetClassifyConcurrency sets the max number of classification batches run in
// parallel. Values <= 0 reset to the default. Not safe to call concurrently
// with classification in flight; intended to be called once at startup.
func SetClassifyConcurrency(n int) {
	if n <= 0 {
		classifyMaxConcurrency = defaultClassifyMaxConcurrency
		return
	}
	classifyMaxConcurrency = n
}

//go:embed prompts/classify.md
var classifyPrompt string

// ClassifyFiles runs the LLM classifier on the given files using the cheap model.
// Returns a map of file path → FileType.
//
// cachedTypes maps file paths to previously cached classifications. Files with
// cached results are skipped. Pass nil to classify everything.
func ClassifyFiles(
	ctx context.Context,
	client ai.Client,
	files []AuditFile,
	cachedTypes map[string]FileType,
	onProgress func(status string),
) (map[string]FileType, error) {
	result := make(map[string]FileType, len(files))

	// Separate cached vs uncached
	var uncached []AuditFile
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

	// Build batches
	batches := buildClassifyBatches(uncached)

	type batchResult struct {
		index   int
		results []FileClassification
		err     error
	}

	resultsCh := make(chan batchResult, len(batches))
	sem := make(chan struct{}, classifyMaxConcurrency)
	var wg sync.WaitGroup

	for i, batch := range batches {
		wg.Add(1)
		go func(i int, batch []AuditFile) {
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

	var errors []string
	for br := range resultsCh {
		if br.err != nil {
			log.Printf("Classification batch %d failed: %v", br.index+1, br.err)
			errors = append(errors, br.err.Error())
			continue
		}
		for _, fc := range br.results {
			result[fc.File] = fc.Type
		}
	}

	// Files not classified (errors or missing from response) get "unknown"
	for _, f := range uncached {
		if _, ok := result[f.Path]; !ok {
			result[f.Path] = FileTypeUnknown
		}
	}

	if onProgress != nil {
		onProgress(fmt.Sprintf("classified %d file(s)", len(uncached)))
	}

	return result, nil
}

// buildClassifyBatches splits files into batches of classifyBatchMaxFiles.
func buildClassifyBatches(files []AuditFile) [][]AuditFile {
	var batches [][]AuditFile
	for i := 0; i < len(files); i += classifyBatchMaxFiles {
		end := i + classifyBatchMaxFiles
		if end > len(files) {
			end = len(files)
		}
		batches = append(batches, files[i:end])
	}
	return batches
}

// classifyBatch sends a batch of files to the LLM for classification.
func classifyBatch(ctx context.Context, client ai.Client, files []AuditFile) ([]FileClassification, error) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Classify these %d file(s):\n\n", len(files)))

	for _, f := range files {
		sb.WriteString(fmt.Sprintf("=== %s ===\n", f.Path))
		// Send first 50 lines only — enough for imports + initial declarations
		lines := strings.SplitN(f.Content, "\n", 51)
		if len(lines) > 50 {
			lines = lines[:50]
		}
		sb.WriteString(strings.Join(lines, "\n"))
		sb.WriteString("\n\n")
	}

	messages := []ai.Message{
		{Role: "user", Content: sb.String()},
	}

	raw, err := client.ChatStream(ctx, classifyPrompt, messages, nil)
	if err != nil {
		return nil, fmt.Errorf("classify LLM call: %w", err)
	}

	return parseClassifyResult(raw)
}

// parseClassifyResult parses the LLM classification response.
func parseClassifyResult(raw string) ([]FileClassification, error) {
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

	// Validate types — replace invalid with unknown
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
