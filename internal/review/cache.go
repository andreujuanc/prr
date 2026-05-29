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
// Key = hash(code_context + aoi_serialized + sorted_focus_categories + sha256(prompt) + sha256(criteria) + priorsHash)
//
// codeContext is the diff (PR mode) or source slice (audit mode) that
// the prompt builder will inline. Folding it into the key means any
// change to the surrounding code yields a fresh cache miss — without
// this, an AOI whose line number is stable could serve stale results
// even after the diff around it changed.
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
func IndividualCacheKey(codeContext string, aoi security.AreaOfInterest, focusCategories []string, priorsHash string) string {
	h := sha256.New()
	h.Write([]byte(codeContext))
	h.Write([]byte{0}) // separator
	h.Write([]byte(serializeAOI(aoi)))
	h.Write([]byte{0})
	h.Write([]byte(serializeFocus(focusCategories)))
	h.Write([]byte{0})
	promptHash := sha256.Sum256([]byte(ai.ReviewIndividualPrompt))
	h.Write(promptHash[:])
	h.Write([]byte{0})
	// Evaluation Criteria content (Shapes echo + Review criteria, scoped
	// to this AOI's (category, subcategory)). Folded in so that editing a
	// category .md file — or the Shapes/Review-criteria scoping itself —
	// invalidates stale entries. The static prompt hash above does not
	// cover this composed-at-runtime section.
	criteriaHash := sha256.Sum256([]byte(scopedCriteria([]security.AreaOfInterest{aoi})))
	h.Write(criteriaHash[:])
	h.Write([]byte{0})
	h.Write([]byte(priorsHash))
	return hex.EncodeToString(h.Sum(nil))[:32] // 32 hex chars = 128 bits, plenty
}

// GroupedCacheKey computes the cache key for a grouped review call.
// Key = hash(all_aoi_serialized + code_context + sorted_focus_categories + sha256(prompt) + sha256(criteria) + priorsHash)
//
// codeContext is the per-file diff blob (PR mode) or per-AOI source
// slice blob (audit mode) — see codeContextDigest for the format.
// Folded into the key so changes to the surrounding diff/source
// invalidate cached results even when the AOI line/concern is stable.
//
// The prompt hash is part of the key so tuning review_grouped.md
// invalidates stale cache entries automatically. priorsHash mirrors
// the IndividualCacheKey contract.
func GroupedCacheKey(aois []security.AreaOfInterest, codeContext string, focusCategories []string, priorsHash string) string {
	h := sha256.New()
	for _, aoi := range aois {
		h.Write([]byte(serializeAOI(aoi)))
		h.Write([]byte{0})
	}
	h.Write([]byte{0})
	h.Write([]byte(codeContext))
	h.Write([]byte{0})
	h.Write([]byte(serializeFocus(focusCategories)))
	h.Write([]byte{0})
	promptHash := sha256.Sum256([]byte(ai.ReviewGroupedPrompt))
	h.Write(promptHash[:])
	h.Write([]byte{0})
	// Evaluation Criteria content for the group's distinct (category,
	// subcategory) pairs — see IndividualCacheKey for the rationale.
	criteriaHash := sha256.Sum256([]byte(scopedCriteria(aois)))
	h.Write(criteriaHash[:])
	h.Write([]byte{0})
	h.Write([]byte(priorsHash))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// codeContextDigest returns a deterministic string from a call's
// inlined code context (FileDiffs for PR mode, AOISources for audit
// mode). Order is deterministic — files are sorted alphabetically and
// AOI contexts iterate in AOI order — so two equal contexts produce
// the same digest. Used as the code-context input to the cache keys.
func codeContextDigest(call ReviewCall) string {
	var sb strings.Builder
	if len(call.FileDiffs) > 0 {
		paths := make([]string, 0, len(call.FileDiffs))
		for p := range call.FileDiffs {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			sb.WriteString(p)
			sb.WriteByte(0)
			sb.WriteString(call.FileDiffs[p])
			sb.WriteByte(0)
		}
	}
	if len(call.AOISources) > 0 {
		for i, c := range call.AOISources {
			fmt.Fprintf(&sb, "aoi[%d]\x00%s\x00", i, c)
		}
	}
	return sb.String()
}

func serializeAOI(aoi security.AreaOfInterest) string {
	return fmt.Sprintf("%s:%d-%d:%s/%s:%s:%s",
		aoi.File, aoi.Line, aoi.EndLine,
		aoi.Category, aoi.Subcategory,
		aoi.Concern, aoi.Context)
}

func serializeFocus(cats []string) string {
	if len(cats) == 0 {
		return ""
	}
	sorted := make([]string, len(cats))
	copy(sorted, cats)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}
