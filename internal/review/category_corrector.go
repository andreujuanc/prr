package review

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
)

// maxCategoryCorrectorAttempts caps the corrector round trips. Two is
// enough in practice; more rarely converges.
const maxCategoryCorrectorAttempts = 2

// validCategorySlugs returns a shared, read-only view of the known
// slugs. Cached because ai.AllCategorySlugs returns a fresh copy each
// call and the set is fixed at package init. Callers MUST NOT mutate
// the returned slice — anyone who needs a mutable copy should call
// ai.AllCategorySlugs directly.
var validCategorySlugs = sync.OnceValue(ai.AllCategorySlugs)

// verifyAndCorrectCategory finds findings with off-list categories and
// runs up to maxCategoryCorrectorAttempts corrector round trips on the
// same chat thread to fix them. Findings still off-list after the
// final attempt fall through to convertRawToTyped, which drops them
// with a logged reason. All errors are non-fatal.
func verifyAndCorrectCategory(
	ctx context.Context,
	cc correctorContext,
	rawResult *rawDeepReviewResult,
) *rawDeepReviewResult {
	if rawResult == nil || len(rawResult.Findings) == 0 {
		return rawResult
	}

	// Cheap typo pre-pass: fix obvious near-misses without an LLM call.
	// Logged so prompt drift toward bad slugs still surfaces.
	applyTypoFixes(cc.call, cc.callIndex, rawResult.Findings)

	messages := make([]ai.Message, 0, len(cc.originalMessages)+1+2*maxCategoryCorrectorAttempts)
	messages = append(messages, cc.originalMessages...)
	messages = append(messages, ai.Message{Role: "assistant", Content: cc.originalRaw})

	for attempt := 1; attempt <= maxCategoryCorrectorAttempts; attempt++ {
		badIdx := findBadCategoryIndexes(rawResult.Findings)
		if len(badIdx) == 0 {
			return rawResult
		}

		log.Printf("review: call %d (%s %s/%s) attempt %d/%d — %d/%d finding(s) with off-list categories",
			cc.callIndex+1, cc.call.Type, cc.call.Category, cc.call.Subcategory,
			attempt, maxCategoryCorrectorAttempts,
			len(badIdx), len(rawResult.Findings))

		correctorMsg := buildCategoryCorrectorMessage(rawResult.Findings, badIdx, attempt)
		messages = append(messages, ai.Message{Role: "user", Content: correctorMsg})

		label := fmt.Sprintf("category-corrector call %d attempt %d", cc.callIndex+1, attempt)
		correctorRaw, err := ai.RetryTransient(ctx, 2, label, func(c context.Context) (string, error) {
			return cc.client.ChatStream(c, cc.systemPrompt, messages, nil)
		})
		if err != nil {
			log.Printf("WARNING: review: category corrector failed permanently (call %d attempt %d): %v — %d off-list finding(s) will be dropped",
				cc.callIndex+1, attempt, err, len(badIdx))
			return rawResult
		}
		messages = append(messages, ai.Message{Role: "assistant", Content: correctorRaw})

		corrections := parseCategoryCorrections(correctorRaw)
		applyCategoryCorrections(rawResult, corrections)
	}
	return rawResult
}

// applyTypoFixes rewrites obvious-typo categories to their nearest
// valid slug in place, skipping the LLM corrector for that finding.
// Only fires when exactly one slug is within edit distance 1 (or 2
// for slugs ≥6 chars). Ambiguous or far-off slugs are left alone for
// the LLM to handle.
func applyTypoFixes(call ReviewCall, callIndex int, findings []rawDeepFinding) {
	allowed := validCategorySlugs()
	for i := range findings {
		raw := strings.ToLower(strings.TrimSpace(findings[i].Category))
		if raw == "" {
			continue
		}
		if _, err := state.ParseCategory(raw); err == nil {
			continue
		}
		fix, ok := nearestSlug(raw, allowed)
		if !ok {
			continue
		}
		log.Printf("review: call %d (%s %s/%s) typo-fixed category %q → %q",
			callIndex+1, call.Type, call.Category, call.Subcategory, findings[i].Category, fix)
		findings[i].Category = fix
	}
}

// nearestSlug returns the single slug in allowed that's within edit
// distance 1 (always accepted) or distance 2 for slugs ≥6 chars (only
// if uniquely closest). Returns ok=false on no match or ambiguity.
func nearestSlug(bad string, allowed []string) (string, bool) {
	bestDist := 3
	secondDist := 3
	best := ""
	for _, s := range allowed {
		d := levenshtein(bad, s)
		if d < bestDist {
			secondDist = bestDist
			bestDist = d
			best = s
		} else if d < secondDist {
			secondDist = d
		}
	}
	switch {
	case bestDist == 1:
		return best, true
	case bestDist == 2 && len(bad) >= 6 && secondDist > bestDist:
		return best, true
	default:
		return "", false
	}
}

// levenshtein returns the edit distance between a and b using the
// two-row dynamic-programming variant. Cheap enough for slug-length
// strings; called O(N findings × M allowed) per review call only when
// findings have bad categories.
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

// findBadCategoryIndexes returns the positions of findings whose raw
// Category string isn't a known category.
func findBadCategoryIndexes(findings []rawDeepFinding) []int {
	var out []int
	for i, f := range findings {
		if _, err := state.ParseCategory(f.Category); err != nil {
			out = append(out, i)
		}
	}
	return out
}

type categoryCorrection struct {
	Index       int    `json:"index"`
	Category    string `json:"category,omitempty"`
	Subcategory string `json:"subcategory,omitempty"`
	Withdraw    bool   `json:"withdraw,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func buildCategoryCorrectorMessage(findings []rawDeepFinding, badIdx []int, attempt int) string {
	var b strings.Builder
	if attempt == 1 {
		b.WriteString("Some findings you emitted have categories outside the allowed list.\n\n")
		b.WriteString("For each indexed finding below, either:\n")
		b.WriteString("  (a) re-emit with a `category` chosen from the allowed list. If you change the category, also provide a `subcategory` that belongs to that category (or leave it empty).\n")
		b.WriteString("  (b) withdraw the finding by setting `withdraw: true` and giving a one-line `reason`.\n\n")
		b.WriteString("Allowed categories:\n")
		for _, s := range validCategorySlugs() {
			b.WriteString("  - " + s + "\n")
		}
	} else {
		// Same allowed list as the previous turn — model has it in
		// context already; re-listing it wastes tokens.
		b.WriteString("Still off-list. Pick from the allowed categories I gave you above, or withdraw.\n")
	}
	b.WriteString("\nFindings needing correction:\n\n")
	for _, i := range badIdx {
		f := findings[i]
		fmt.Fprintf(&b, "  - index %d: %s — current category %q, subcategory %q\n", i, f.Title, f.Category, f.Subcategory)
	}
	b.WriteString("\nReturn ONLY a JSON object, no prose:\n\n")
	b.WriteString("```json\n")
	b.WriteString("{\n")
	b.WriteString("  \"corrections\": [\n")
	b.WriteString("    {\"index\": 0, \"category\": \"correctness\", \"subcategory\": \"off-by-one\"},\n")
	b.WriteString("    {\"index\": 1, \"withdraw\": true, \"reason\": \"no listed category fits this concern\"}\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n")
	b.WriteString("```\n\n")
	b.WriteString("Only include entries for the indexes listed above.")
	return b.String()
}

func parseCategoryCorrections(raw string) []categoryCorrection {
	extracted, err := extractLastJSONValue([]byte(ai.StripMarkdownFences(raw)))
	if err != nil {
		log.Printf("review: category corrector response had no JSON; no corrections applied")
		return nil
	}
	var resp struct {
		Corrections []categoryCorrection `json:"corrections"`
	}
	if err := unmarshalLLMResponse(extracted, &resp); err != nil {
		log.Printf("review: failed to parse category corrector response: %v", err)
		return nil
	}
	return resp.Corrections
}

func applyCategoryCorrections(raw *rawDeepReviewResult, corrections []categoryCorrection) {
	if len(corrections) == 0 {
		return
	}
	withdrawn := make(map[int]string)
	for _, c := range corrections {
		if c.Index < 0 || c.Index >= len(raw.Findings) {
			log.Printf("review: category corrector returned out-of-range index %d (have %d findings)",
				c.Index, len(raw.Findings))
			continue
		}
		if c.Withdraw {
			withdrawn[c.Index] = c.Reason
			continue
		}
		if c.Category != "" {
			raw.Findings[c.Index].Category = c.Category
			// Subcategory belongs to the category — when category
			// changes, replace subcategory with whatever the model
			// provides (empty string clears it). Carrying the old
			// subcategory forward would yield mismatched pairs.
			raw.Findings[c.Index].Subcategory = c.Subcategory
		}
	}
	if len(withdrawn) == 0 {
		return
	}
	out := raw.Findings[:0]
	for i, f := range raw.Findings {
		if reason, ok := withdrawn[i]; ok {
			if reason != "" {
				log.Printf("review: finding withdrawn by category corrector — %s (%s)", f.Title, reason)
			} else {
				log.Printf("review: finding withdrawn by category corrector — %s", f.Title)
			}
			continue
		}
		out = append(out, f)
	}
	raw.Findings = out
}
