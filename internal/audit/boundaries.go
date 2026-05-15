package audit

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

//go:embed prompts/boundary_inventory.md
var boundaryInventorySystemPrompt string

// boundaryHeaderLineCap caps the per-file header excerpt fed to the
// LLM. 80 lines is enough to see route declarations, queue
// subscriptions, scheduled triggers, and the imports needed to
// classify them — without pulling whole files.
const boundaryHeaderLineCap = 80

// boundaryInventoryMaxFiles bounds the candidate file list. The
// boundary discovery prompt is a single LLM call, so its input must
// fit one context window. 200 files * ~80 lines * ~80 chars/line
// ~= 1.3MB of source — well under typical context budgets but large
// enough that we cap defensively.
const boundaryInventoryMaxFiles = 200

// BoundaryDiscoveryResult holds the output of one Phase 1.5 pass.
type BoundaryDiscoveryResult struct {
	// Boundaries is the parsed boundary list. Nil when loaded from
	// cache (caller already holds the cached value).
	Boundaries []state.Boundary

	// InputHash is a SHA-256 hash of the inputs used to generate the
	// inventory. Used for cache invalidation.
	InputHash string

	// FromCache indicates the cached hash matched; Boundaries is
	// nil in that case.
	FromCache bool
}

// DiscoverBoundaries runs Phase 1.5: one LLM call that takes file
// header excerpts + the runtime model (when available) and emits a
// structured list of externally-reachable surfaces.
//
// The runtime model is optional — passing nil falls back to "look at
// the file headers and classify what you see." Quality is better
// when the runtime model is present because its entry-point classes
// anchor the classification.
//
// On LLM/parse failure returns an error; the caller treats Phase 1.5
// as best-effort and proceeds without the inventory.
func DiscoverBoundaries(
	ctx context.Context,
	client ai.Client,
	files map[string]string,
	runtimeModel *state.RuntimeModel,
	cachedHash string,
	onProgress func(string),
) (*BoundaryDiscoveryResult, error) {
	if onProgress == nil {
		onProgress = func(string) {}
	}
	if client == nil {
		return nil, fmt.Errorf("nil ai.Client")
	}
	if len(files) == 0 {
		return &BoundaryDiscoveryResult{}, nil
	}

	onProgress("Discovering boundary inventory...")

	excerpts := buildBoundaryExcerpts(files, boundaryHeaderLineCap, boundaryInventoryMaxFiles)
	if len(excerpts) == 0 {
		return &BoundaryDiscoveryResult{}, nil
	}

	inputHash := hashBoundaryInputs(excerpts, runtimeModel)
	if cachedHash != "" && cachedHash == inputHash {
		onProgress("Boundary inventory unchanged (cache hit)")
		return &BoundaryDiscoveryResult{InputHash: inputHash, FromCache: true}, nil
	}

	onProgress("Scanning file headers for boundaries...")
	boundaries, err := summarizeBoundaries(ctx, client, excerpts, runtimeModel)
	if err != nil {
		return nil, err
	}

	normalizeBoundaries(boundaries)

	onProgress(fmt.Sprintf("Boundary inventory ready (%d boundaries)", len(boundaries)))
	return &BoundaryDiscoveryResult{
		Boundaries: boundaries,
		InputHash:  inputHash,
	}, nil
}

// buildBoundaryExcerpts trims each file's content to the first
// `lineCap` lines and orders the result deterministically (alphabetic
// by path) so the hash is stable across runs. When the file count
// exceeds maxFiles we drop the longest paths first — those tend to
// be deeply-nested helpers, less likely to declare boundaries.
func buildBoundaryExcerpts(files map[string]string, lineCap, maxFiles int) []boundaryExcerpt {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	if len(paths) > maxFiles {
		paths = paths[:maxFiles]
	}

	out := make([]boundaryExcerpt, 0, len(paths))
	for _, p := range paths {
		body := files[p]
		if body == "" {
			continue
		}
		out = append(out, boundaryExcerpt{Path: p, Header: trimToLines(body, lineCap)})
	}
	return out
}

type boundaryExcerpt struct {
	Path   string
	Header string
}

// trimToLines returns the first n lines of s (newline-separated).
// When s has fewer lines, returns s unchanged.
func trimToLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			count++
			if count == n {
				return s[:i]
			}
		}
	}
	return s
}

// hashBoundaryInputs hashes the ordered excerpts plus the runtime
// model (when present) plus the boundary-inventory prompt itself.
// Mixing the prompt in means a later edit to boundary_inventory.md
// auto-invalidates the cached inventory rather than silently serving
// boundaries produced by the previous prompt.
func hashBoundaryInputs(excerpts []boundaryExcerpt, model *state.RuntimeModel) string {
	h := sha256.New()
	for _, e := range excerpts {
		h.Write([]byte(e.Path))
		h.Write([]byte{0})
		h.Write([]byte(e.Header))
		h.Write([]byte{0})
	}
	if model != nil {
		if data, err := json.Marshal(model); err == nil {
			h.Write([]byte{0})
			h.Write(data)
		}
	}
	h.Write([]byte{0})
	promptHash := sha256.Sum256([]byte(boundaryInventorySystemPrompt))
	h.Write(promptHash[:])
	return fmt.Sprintf("%x", h.Sum(nil))
}

// summarizeBoundaries calls the LLM with the boundary-inventory
// system prompt and a user message built from the excerpts. The
// runtime model (when present) is rendered into the user message so
// the LLM has the codebase's entry-point classes for context.
func summarizeBoundaries(
	ctx context.Context,
	client ai.Client,
	excerpts []boundaryExcerpt,
	model *state.RuntimeModel,
) ([]state.Boundary, error) {
	var user strings.Builder

	if rendered := model.Render(); rendered != "" {
		user.WriteString("=== Runtime Model ===\n")
		user.WriteString(rendered)
		user.WriteString("\n\n")
	}

	user.WriteString("=== File Headers ===\n\n")
	for _, e := range excerpts {
		fmt.Fprintf(&user, "--- %s ---\n%s\n\n", e.Path, e.Header)
	}

	messages := []ai.Message{{Role: "user", Content: user.String()}}

	raw, err := client.ChatStream(ctx, boundaryInventorySystemPrompt, messages, nil)
	if err != nil {
		return nil, fmt.Errorf("LLM call: %w", err)
	}

	js := extractJSONArray(raw)
	if js == "" {
		return nil, fmt.Errorf("no JSON array in LLM response (len=%d)", len(raw))
	}

	var out []state.Boundary
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return out, nil
}

// extractJSONArray pulls the first balanced `[...]` substring from raw
// LLM output. Tolerates leading/trailing prose and a wrapping markdown
// fence. Returns "" if no plausible array is present.
func extractJSONArray(raw string) string {
	s := strings.TrimSpace(raw)

	// Strip a leading fenced block if present.
	if strings.HasPrefix(s, "```") {
		if idx := strings.IndexByte(s, '\n'); idx != -1 {
			s = s[idx+1:]
		}
		if i := strings.LastIndex(s, "```"); i != -1 {
			s = s[:i]
		}
	}

	start := strings.IndexByte(s, '[')
	if start < 0 {
		return ""
	}

	depth := 0
	inStr := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			if escape {
				escape = false
				continue
			}
			switch c {
			case '\\':
				escape = true
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// normalizeBoundaries cleans up LLM output: lowercases Kind, trims
// whitespace, and drops entries that don't carry a kind or file.
func normalizeBoundaries(in []state.Boundary) []state.Boundary {
	out := in[:0]
	for _, b := range in {
		b.Kind = strings.ToLower(strings.TrimSpace(b.Kind))
		b.File = strings.TrimSpace(b.File)
		b.Lines = strings.TrimSpace(b.Lines)
		b.Symbol = strings.TrimSpace(b.Symbol)
		b.Description = strings.TrimSpace(b.Description)
		if b.Kind == "" || b.File == "" {
			continue
		}
		out = append(out, b)
	}
	return out
}

// SynthesizeBoundaryAOIs turns a boundary inventory into a slice of
// AreaOfInterest entries that ensure every boundary is reviewed for
// the standard defense classes. The AOIs are deterministic
// (no LLM) — for each boundary we emit one AOI per defense question
// appropriate to the kind.
//
// The returned slice groups AOIs by File so the caller can merge
// them into per-file AOIScanResult records without re-sorting.
func SynthesizeBoundaryAOIs(inventory []state.Boundary) []security.AreaOfInterest {
	var out []security.AreaOfInterest
	for _, b := range inventory {
		out = append(out, defenseAOIsForBoundary(b)...)
	}
	return out
}

// defenseAOIsForBoundary returns the synthetic AOIs that cover the
// defense questions for one boundary. The set varies by kind:
//
//   - http / rpc / webhook: schema validation, authorization, error
//     handling. These boundaries take untrusted input from external
//     callers, so all three defenses matter.
//   - queue: schema validation, per-record isolation, result
//     discipline. The "per-record isolation" question is unique to
//     batched consumers — one bad record must not fail the batch.
//   - scheduled / cli / other: result discipline only — the inputs
//     are typically internally-controlled, but the publish/write
//     path still needs to propagate errors correctly.
func defenseAOIsForBoundary(b state.Boundary) []security.AreaOfInterest {
	var defenses []boundaryDefense
	switch b.Kind {
	case "http", "rpc", "webhook":
		defenses = []boundaryDefense{
			{
				slug:     "schema-validation",
				category: "input-validation",
				subcat:   "boundary-coverage",
				concern:  "Is the request payload validated against a declared schema at the boundary?",
			},
			{
				slug:     "authz",
				category: "authorization",
				subcat:   "boundary-coverage",
				concern:  "Is authentication and authorization enforced at this boundary (or upstream in the gateway/middleware chain)?",
			},
			{
				slug:     "error-handling",
				category: "error-handling",
				subcat:   "boundary-coverage",
				concern:  "Does the handler return a structured error response on parse failures and downstream errors, rather than leaking stack traces or 500s?",
			},
		}
	case "queue":
		defenses = []boundaryDefense{
			{
				slug:     "schema-validation",
				category: "input-validation",
				subcat:   "boundary-coverage",
				concern:  "Is each message validated against a declared schema before reaching business logic?",
			},
			{
				slug:     "per-record-isolation",
				category: "external-io",
				subcat:   "boundary-coverage",
				concern:  "When a batch arrives, does one bad record fail only itself, or does the whole batch get retried (and risk poison-pill loops)?",
			},
			{
				slug:     "result-discipline",
				category: "error-handling",
				subcat:   "boundary-coverage",
				concern:  "On the publish/write path: are errors propagated to the queue redrive mechanism, or silently swallowed?",
			},
		}
	case "scheduled":
		defenses = []boundaryDefense{
			{
				slug:     "result-discipline",
				category: "error-handling",
				subcat:   "boundary-coverage",
				concern:  "Are errors during the scheduled run surfaced (alerting, retry, dead-letter), or does the job silently fail until someone notices?",
			},
			{
				slug:     "concurrency-guard",
				category: "concurrency",
				subcat:   "boundary-coverage",
				concern:  "If the schedule fires while a prior run is still in progress, is there a lock/lease/idempotency guard to prevent concurrent execution corrupting shared state?",
			},
		}
	case "cli":
		defenses = []boundaryDefense{
			{
				slug:     "schema-validation",
				category: "input-validation",
				subcat:   "boundary-coverage",
				concern:  "Are CLI arguments validated (types, bounds, allowed values) before reaching business logic?",
			},
		}
	default:
		// "other" and unknown kinds: at minimum, result discipline.
		defenses = []boundaryDefense{
			{
				slug:     "result-discipline",
				category: "error-handling",
				subcat:   "boundary-coverage",
				concern:  "Does the boundary propagate errors to its caller (and onward to observability) rather than swallowing them?",
			},
		}
	}

	out := make([]security.AreaOfInterest, 0, len(defenses))
	for _, d := range defenses {
		out = append(out, security.AreaOfInterest{
			ID:          boundaryAOIID(b, d.slug),
			File:        b.File,
			Line:        firstLineFromRange(b.Lines),
			EndLine:     lastLineFromRange(b.Lines),
			Category:    d.category,
			Subcategory: d.subcat,
			Urgency:     "grouped",
			Concern:     d.concern,
			Context:     boundaryAOIContext(b),
			Dimensions:  []string{d.category},
		})
	}
	return out
}

// boundaryDefense is the per-defense-question template a boundary
// kind expands into.
type boundaryDefense struct {
	slug     string
	category string
	subcat   string
	concern  string
}

// boundaryAOIID builds a stable, slug-shaped id for a synthetic AOI.
// IDs feed caching and cross-referencing so the same boundary +
// defense question yields the same id across runs.
func boundaryAOIID(b state.Boundary, defenseSlug string) string {
	// Hash the boundary's file+kind+symbol so two boundaries on the
	// same file (e.g. POST /a and POST /b) don't collide.
	h := sha256.Sum256([]byte(b.File + "|" + b.Kind + "|" + b.Symbol))
	tag := fmt.Sprintf("%x", h)[:10]
	return fmt.Sprintf("boundary-%s-%s-%s", slugifyKind(b.Kind), defenseSlug, tag)
}

// slugifyKind normalises a boundary Kind into the slug shape the
// AOI id matcher expects: [a-z0-9-]+. The Kind already comes in
// lowercase after normalizeBoundaries; the extra step is defensive.
func slugifyKind(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	if k == "" {
		return "other"
	}
	return k
}

// boundaryAOIContext renders the human-readable context line that
// accompanies a synthetic AOI: the boundary description plus any
// available symbol.
func boundaryAOIContext(b state.Boundary) string {
	var parts []string
	if b.Description != "" {
		parts = append(parts, b.Description)
	}
	if b.Symbol != "" {
		parts = append(parts, fmt.Sprintf("symbol: %s", b.Symbol))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Boundary of kind %s in %s", b.Kind, b.File)
	}
	return strings.Join(parts, " — ")
}

// firstLineFromRange parses the "start" of a "start-end" range
// string, returning 1 when no valid integer is found. AOIs need at
// least a Line=1 anchor for Phase 3 prompts.
func firstLineFromRange(s string) int {
	if s == "" {
		return 1
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s = s[:i]
	}
	n, err := atoiSafe(s)
	if err != nil || n <= 0 {
		return 1
	}
	return n
}

// lastLineFromRange parses the "end" of a "start-end" range, returning
// 0 when the range is a single line (matching AreaOfInterest's
// "EndLine omitempty when EndLine==Line" convention).
func lastLineFromRange(s string) int {
	if s == "" {
		return 0
	}
	i := strings.IndexByte(s, '-')
	if i < 0 {
		return 0
	}
	n, err := atoiSafe(strings.TrimSpace(s[i+1:]))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// atoiSafe trims whitespace and parses an int without panicking on
// non-numeric input.
func atoiSafe(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

// MergeBoundaryAOIs inserts synthetic boundary-coverage AOIs into the
// per-file AOI scan results. AOIs are appended to the matching
// file's existing AreasOfInterest list (creating a new
// AOIScanResult when the file isn't in `results` already).
//
// Returns the merged result list. Idempotency: if an AOI with the
// same ID already exists in the file, the synthetic AOI is dropped
// rather than duplicated. This keeps the merge safe to re-run on
// already-cached results.
func MergeBoundaryAOIs(results []security.AOIScanResult, boundaryAOIs []security.AreaOfInterest) []security.AOIScanResult {
	if len(boundaryAOIs) == 0 {
		return results
	}

	byFile := make(map[string]int, len(results))
	for i, r := range results {
		byFile[r.File] = i
	}

	for _, aoi := range boundaryAOIs {
		idx, ok := byFile[aoi.File]
		if !ok {
			results = append(results, security.AOIScanResult{
				File:            aoi.File,
				AreasOfInterest: []security.AreaOfInterest{aoi},
			})
			byFile[aoi.File] = len(results) - 1
			continue
		}
		// Skip when an AOI with the same id already exists — keeps
		// the merge idempotent if called twice on the same input.
		exists := false
		for _, existing := range results[idx].AreasOfInterest {
			if existing.ID == aoi.ID {
				exists = true
				break
			}
		}
		if exists {
			continue
		}
		results[idx].AreasOfInterest = append(results[idx].AreasOfInterest, aoi)
	}
	return results
}
