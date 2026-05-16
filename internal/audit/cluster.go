package audit

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/security"
	"github.com/andreujuanc/prr/internal/state"
)

//go:embed prompts/sibling_cluster.md
var siblingClusterSystemPrompt string

// siblingClusterGlobalThreshold is the total-AOI count above which
// the clusterer splits its work per category (parallel calls) rather
// than running one global call. The global call gives the LLM the
// best context for spotting cross-category patterns, but past this
// threshold the prompt size becomes the dominant cost.
const siblingClusterGlobalThreshold = 200

// siblingClusterMinSize is the minimum cluster size that can yield an
// outlier. Below this, "deviation" is just coincidence.
const siblingClusterMinSize = 5

// SiblingClusterResult holds the output of one Phase 2.5 pass.
type SiblingClusterResult struct {
	// Outliers is the list of synthesized outlier AOIs (urgency
	// "individual", SiblingDeviation populated) ready to merge into
	// the per-file scan results.
	Outliers []security.AreaOfInterest

	// InputHash is a SHA-256 hash of the AOI inputs + runtime model.
	InputHash string

	// FromCache indicates the cached hash matched; Outliers is nil
	// in that case (caller uses the cached value it already holds).
	FromCache bool
}

// clusterCandidate is the per-AOI projection sent to the LLM. It
// strips out file content and dimensions to keep the prompt compact
// — the LLM only needs the AOI's category, concern, and context to
// judge similarity.
type clusterCandidate struct {
	ID          string `json:"id"`
	File        string `json:"file"`
	Category    string `json:"category"`
	Subcategory string `json:"subcategory,omitempty"`
	Concern     string `json:"concern"`
	Context     string `json:"context,omitempty"`
}

// clusterLLMResult mirrors the JSON shape the prompt asks the LLM to
// produce.
type clusterLLMResult struct {
	Pattern          string   `json:"pattern"`
	SiblingIDs       []string `json:"sibling_ids"`
	DeviantIDs       []string `json:"deviant_ids"`
	Category         string   `json:"category"`
	DeviationConcern string   `json:"deviation_concern"`
}

// DiscoverSiblingOutliers runs Phase 2.5: it groups AOIs by shape via
// LLM clustering, identifies deviants whose pattern disagrees with
// their siblings, and synthesizes outlier AOIs (urgency individual,
// SiblingDeviation populated) for Phase 3.
//
// Above siblingClusterGlobalThreshold total AOIs the work splits per
// category and runs in parallel — one LLM call per category — so no
// single prompt has to hold the full AOI set. Below the threshold a
// single global call gives the LLM cross-category visibility.
//
// On LLM/parse failure for any single call this function logs and
// keeps going with whatever results other calls produced — sibling
// clustering is a quality enhancement, not a load-bearing input.
// Returns nil Outliers when no clusters meet the threshold.
func DiscoverSiblingOutliers(
	ctx context.Context,
	client ai.Client,
	aois []security.AreaOfInterest,
	cachedHash string,
	onProgress func(string),
) (*SiblingClusterResult, error) {
	if onProgress == nil {
		onProgress = func(string) {}
	}
	if client == nil {
		return nil, fmt.Errorf("nil ai.Client")
	}
	if len(aois) == 0 {
		return &SiblingClusterResult{}, nil
	}

	onProgress("Identifying sibling clusters...")

	candidates := projectCandidates(aois)
	if len(candidates) == 0 {
		return &SiblingClusterResult{}, nil
	}

	inputHash := hashClusterInputs(candidates)
	if cachedHash != "" && cachedHash == inputHash {
		onProgress("Sibling cluster analysis unchanged (cache hit)")
		return &SiblingClusterResult{InputHash: inputHash, FromCache: true}, nil
	}

	aoiByID := make(map[string]security.AreaOfInterest, len(aois))
	for _, a := range aois {
		if a.ID != "" {
			aoiByID[a.ID] = a
		}
	}

	var clusters []clusterLLMResult
	if len(candidates) > siblingClusterGlobalThreshold {
		// Above the threshold: cluster per (category, subcategory) in
		// parallel under the same cap as Phase 2 / classify.
		clusters = runPerCategoryClustering(ctx, client, candidates, onProgress)
	} else {
		// Below the threshold: one global call.
		global, err := runOneClusterCall(ctx, client, candidates)
		if err != nil {
			return nil, fmt.Errorf("sibling clustering: %w", err)
		}
		clusters = global
	}

	outliers := synthesizeOutlierAOIs(clusters, aoiByID)
	onProgress(fmt.Sprintf("Sibling clustering ready (%d outlier AOIs from %d clusters)",
		len(outliers), len(clusters)))
	return &SiblingClusterResult{
		Outliers:  outliers,
		InputHash: inputHash,
	}, nil
}

// runOneClusterCall runs a single LLM call on the full candidate set
// and returns parsed clusters. Errors are wrapped and returned —
// callers downgrade to non-fatal.
func runOneClusterCall(ctx context.Context, client ai.Client, candidates []clusterCandidate) ([]clusterLLMResult, error) {
	user, err := json.Marshal(candidates)
	if err != nil {
		return nil, fmt.Errorf("marshal candidates: %w", err)
	}
	messages := []ai.Message{{Role: "user", Content: string(user)}}

	// Retry transient errors. Sibling clustering is experimental and
	// fail-soft; retrying still helps when a transient blip is the
	// only thing preventing cluster discovery.
	raw, err := ai.RetryTransient(ctx, 3, "audit-cluster", func(ctx context.Context) (string, error) {
		return client.ChatStream(ctx, siblingClusterSystemPrompt, messages, nil)
	})
	if err != nil {
		return nil, fmt.Errorf("LLM call: %w", err)
	}
	js := extractJSONArray(raw)
	if js == "" {
		return nil, fmt.Errorf("no JSON array in LLM response")
	}
	var out []clusterLLMResult
	if err := json.Unmarshal([]byte(js), &out); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return out, nil
}

// runPerCategoryClustering splits candidates by (category,
// subcategory) and runs one LLM call per partition in parallel under
// a bounded semaphore. Partition failures log and drop; surviving
// partitions still contribute. Returns the concatenated cluster list
// across all successful partitions.
func runPerCategoryClustering(
	ctx context.Context,
	client ai.Client,
	candidates []clusterCandidate,
	onProgress func(string),
) []clusterLLMResult {
	groups := groupByCategory(candidates)

	var wg sync.WaitGroup
	sem := make(chan struct{}, aoiMaxConcurrencyForCluster())

	resultsCh := make(chan []clusterLLMResult, len(groups))

	for label, members := range groups {
		// Skip partitions below the minimum cluster size — the LLM
		// has nothing to find there.
		if len(members) < siblingClusterMinSize {
			continue
		}
		wg.Add(1)
		go func(label string, members []clusterCandidate) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			clusters, err := runOneClusterCall(ctx, client, members)
			if err != nil {
				onProgress(fmt.Sprintf("sibling cluster %s failed (non-fatal): %v", label, err))
				return
			}
			resultsCh <- clusters
		}(label, members)
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	var all []clusterLLMResult
	for cs := range resultsCh {
		all = append(all, cs...)
	}
	return all
}

// clusterMaxConcurrency is the parallelism cap for per-category
// cluster calls. Matches Phase 2's AOI scanner default — clustering
// is rare (one call per category, ~20 categories on a large repo)
// so going higher offers diminishing returns.
const clusterMaxConcurrency = 5

// aoiMaxConcurrencyForCluster returns the parallelism cap for
// per-category cluster calls. Returns the package default; exposed
// as a function so future commits can wire it to user config
// without touching every call site.
func aoiMaxConcurrencyForCluster() int {
	return clusterMaxConcurrency
}

// projectCandidates converts security.AreaOfInterest to the compact
// clusterCandidate shape. Drops AOIs without an ID (they can't be
// referenced from the cluster output).
func projectCandidates(aois []security.AreaOfInterest) []clusterCandidate {
	out := make([]clusterCandidate, 0, len(aois))
	for _, a := range aois {
		if a.ID == "" {
			continue
		}
		out = append(out, clusterCandidate{
			ID:          a.ID,
			File:        a.File,
			Category:    a.Category,
			Subcategory: a.Subcategory,
			Concern:     a.Concern,
			Context:     a.Context,
		})
	}
	return out
}

// groupByCategory groups candidates by "category/subcategory". Keys
// are formatted as "category/subcategory" when subcategory is
// non-empty, otherwise just "category".
func groupByCategory(candidates []clusterCandidate) map[string][]clusterCandidate {
	out := make(map[string][]clusterCandidate, 16)
	for _, c := range candidates {
		key := c.Category
		if c.Subcategory != "" {
			key = c.Category + "/" + c.Subcategory
		}
		out[key] = append(out[key], c)
	}
	return out
}

// hashClusterInputs hashes the ordered candidates plus the cluster
// prompt itself so the cache invalidates when the AOI set OR the
// prompt rules change. Without the prompt hash a later edit to
// sibling_cluster.md would silently serve clusters produced by the
// previous prompt.
func hashClusterInputs(candidates []clusterCandidate) string {
	sorted := make([]clusterCandidate, len(candidates))
	copy(sorted, candidates)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	h := sha256.New()
	for _, c := range sorted {
		fmt.Fprintf(h, "%s|%s|%s|%s|%s\x00", c.ID, c.File, c.Category, c.Concern, c.Context)
	}
	h.Write([]byte{0})
	promptHash := sha256.Sum256([]byte(siblingClusterSystemPrompt))
	h.Write(promptHash[:])
	return fmt.Sprintf("%x", h.Sum(nil))
}

// synthesizeOutlierAOIs turns the LLM's cluster output into outlier
// AOIs ready to merge with the Phase 2 scan results. Skips clusters
// that don't meet the size + deviation requirements (defensive — the
// prompt enforces them but the model can drift).
func synthesizeOutlierAOIs(clusters []clusterLLMResult, aoiByID map[string]security.AreaOfInterest) []security.AreaOfInterest {
	var out []security.AreaOfInterest
	for _, c := range clusters {
		if len(c.SiblingIDs)+len(c.DeviantIDs) < siblingClusterMinSize {
			continue
		}
		if len(c.DeviantIDs) == 0 {
			continue
		}
		// Refuse runaway "deviations" — if half the cluster deviates
		// it isn't a deviation, just two competing patterns.
		if len(c.DeviantIDs) >= len(c.SiblingIDs) {
			continue
		}

		// Verify the deviant IDs actually map to known AOIs. Anchor
		// to the original AOI's file/line so Phase 3 can read code
		// at the right location.
		for _, did := range c.DeviantIDs {
			base, ok := aoiByID[did]
			if !ok {
				continue
			}
			outlier := security.AreaOfInterest{
				ID:          deviantAOIID(did, c.Pattern),
				File:        base.File,
				Line:        base.Line,
				EndLine:     base.EndLine,
				Category:    c.Category,
				Subcategory: "sibling-deviation",
				Urgency:     "individual",
				Concern:     c.DeviationConcern,
				Context:     fmt.Sprintf("Sibling pattern: %s", strings.TrimSpace(c.Pattern)),
				Dimensions:  []string{c.Category},
				SiblingDeviation: &state.SiblingDeviation{
					Pattern:    c.Pattern,
					SiblingIDs: c.SiblingIDs,
				},
			}
			out = append(out, outlier)
		}
	}
	return out
}

// deviantAOIID builds a stable slug-shaped id for a synthesized
// outlier AOI. Mixing the source AOI id with a short hash of the
// pattern lets two distinct clusters that both name the same source
// AOI as a deviant produce different ids.
func deviantAOIID(sourceID, pattern string) string {
	h := sha256.Sum256([]byte(sourceID + "|" + pattern))
	tag := fmt.Sprintf("%x", h)[:10]
	return fmt.Sprintf("deviant-%s-%s", slugifyShort(sourceID), tag)
}

// slugifyShort returns a slug-friendly truncation of s. AOI ids are
// already in [a-z0-9-]+ shape (the AOI scanner enforces it), so the
// only transform we need is trimming length.
func slugifyShort(s string) string {
	const maxLen = 40
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
