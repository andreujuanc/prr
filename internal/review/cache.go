package review

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/security"
)

// IndividualCacheKey computes the cache key for an individual review call.
// Key = hash(file_content + aoi_serialized + sorted_focus_dimensions + sha256(prompt) + priorsHash)
//
// The prompt hash is part of the key so tuning review_individual.md
// invalidates stale cache entries automatically. Without it, a prompt
// change would silently serve results produced by the previous prompt
// for the duration of the cached entry. Same pattern as
// computeRecheckCacheKey in audit/pipeline.go.
//
// priorsHash is sha256 of the bug-priors content injected at runtime
// when --bug-priors is enabled (empty when disabled). Folded in here
// so new fix commits → new priors → new hash → fresh review, even
// when file content and AOI are unchanged. The parameter's presence
// changes the key shape from the legacy version, so the first run
// after this lands is a one-time re-review; subsequent flag-off runs
// all match each other.
func IndividualCacheKey(fileContent string, aoi security.AreaOfInterest, focusDimensions []string, priorsHash string) string {
	h := sha256.New()
	h.Write([]byte(fileContent))
	h.Write([]byte{0}) // separator
	h.Write([]byte(serializeAOI(aoi)))
	h.Write([]byte{0})
	h.Write([]byte(serializeFocus(focusDimensions)))
	h.Write([]byte{0})
	promptHash := sha256.Sum256([]byte(ai.ReviewIndividualPrompt))
	h.Write(promptHash[:])
	h.Write([]byte{0})
	h.Write([]byte(priorsHash))
	return hex.EncodeToString(h.Sum(nil))[:32] // 32 hex chars = 128 bits, plenty
}

// GroupedCacheKey computes the cache key for a grouped review call.
// Key = hash(all_aoi_serialized + sorted_focus_dimensions + sha256(prompt) + priorsHash)
// If any file in the group changes, the key changes because file content
// is not included — the AOI content itself changes when file content changes
// (since AOIs are regenerated on file change).
//
// The prompt hash is part of the key so tuning review_grouped.md
// invalidates stale cache entries automatically. priorsHash mirrors
// the IndividualCacheKey contract.
func GroupedCacheKey(aois []security.AreaOfInterest, focusDimensions []string, priorsHash string) string {
	h := sha256.New()
	for _, aoi := range aois {
		h.Write([]byte(serializeAOI(aoi)))
		h.Write([]byte{0})
	}
	h.Write([]byte{0})
	h.Write([]byte(serializeFocus(focusDimensions)))
	h.Write([]byte{0})
	promptHash := sha256.Sum256([]byte(ai.ReviewGroupedPrompt))
	h.Write(promptHash[:])
	h.Write([]byte{0})
	h.Write([]byte(priorsHash))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

func serializeAOI(aoi security.AreaOfInterest) string {
	return fmt.Sprintf("%s:%d-%d:%s/%s:%s:%s",
		aoi.File, aoi.Line, aoi.EndLine,
		aoi.Category, aoi.Subcategory,
		aoi.Concern, aoi.Context)
}

func serializeFocus(dims []string) string {
	if len(dims) == 0 {
		return ""
	}
	sorted := make([]string, len(dims))
	copy(sorted, dims)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}
