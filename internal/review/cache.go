package review

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/security"
)

// IndividualCacheKey computes the cache key for an individual review call.
// Key = hash(file_content + aoi_serialized + sorted_focus_dimensions)
func IndividualCacheKey(fileContent string, aoi security.AreaOfInterest, focusDimensions []string) string {
	h := sha256.New()
	h.Write([]byte(fileContent))
	h.Write([]byte{0}) // separator
	h.Write([]byte(serializeAOI(aoi)))
	h.Write([]byte{0})
	h.Write([]byte(serializeFocus(focusDimensions)))
	return hex.EncodeToString(h.Sum(nil))[:32] // 32 hex chars = 128 bits, plenty
}

// GroupedCacheKey computes the cache key for a grouped review call.
// Key = hash(all_aoi_serialized + sorted_focus_dimensions)
// If any file in the group changes, the key changes because file content
// is not included — the AOI content itself changes when file content changes
// (since AOIs are regenerated on file change).
func GroupedCacheKey(aois []security.AreaOfInterest, focusDimensions []string) string {
	h := sha256.New()
	for _, aoi := range aois {
		h.Write([]byte(serializeAOI(aoi)))
		h.Write([]byte{0})
	}
	h.Write([]byte{0})
	h.Write([]byte(serializeFocus(focusDimensions)))
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
