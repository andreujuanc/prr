package review

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/andreujuanc/prr/internal/ai"
	"github.com/andreujuanc/prr/internal/state"
)

// maxCategoryCorrectorAttempts caps the corrector round trips. Two is
// enough in practice; more rarely converges.
const maxCategoryCorrectorAttempts = 2

// verifyAndCorrectCategory finds findings with off-list categories and
// runs up to maxCategoryCorrectorAttempts corrector round trips on the
// same chat thread to fix them. Findings still off-list after the
// final attempt fall through to convertRawToTyped, which drops them
// with a logged reason. All errors are non-fatal.
func verifyAndCorrectCategory(
	ctx context.Context,
	client ai.Client,
	call ReviewCall,
	callIndex int,
	systemPrompt string,
	originalMessages []ai.Message,
	originalRaw string,
	rawResult *rawDeepReviewResult,
) *rawDeepReviewResult {
	if rawResult == nil || len(rawResult.Findings) == 0 {
		return rawResult
	}

	messages := make([]ai.Message, 0, len(originalMessages)+1+2*maxCategoryCorrectorAttempts)
	messages = append(messages, originalMessages...)
	messages = append(messages, ai.Message{Role: "assistant", Content: originalRaw})

	for attempt := 1; attempt <= maxCategoryCorrectorAttempts; attempt++ {
		badIdx := findBadCategoryIndexes(rawResult.Findings)
		if len(badIdx) == 0 {
			return rawResult
		}

		log.Printf("review: call %d (%s %s/%s) attempt %d/%d — %d/%d finding(s) with off-list categories",
			callIndex+1, call.Type, call.Category, call.Subcategory,
			attempt, maxCategoryCorrectorAttempts,
			len(badIdx), len(rawResult.Findings))

		correctorMsg := buildCategoryCorrectorMessage(rawResult.Findings, badIdx)
		messages = append(messages, ai.Message{Role: "user", Content: correctorMsg})

		label := fmt.Sprintf("category-corrector call %d attempt %d", callIndex+1, attempt)
		correctorRaw, err := ai.RetryTransient(ctx, 2, label, func(c context.Context) (string, error) {
			return client.ChatStream(c, systemPrompt, messages, nil)
		})
		if err != nil {
			log.Printf("WARNING: review: category corrector failed permanently (call %d attempt %d): %v — %d off-list finding(s) will be dropped",
				callIndex+1, attempt, err, len(badIdx))
			return rawResult
		}
		messages = append(messages, ai.Message{Role: "assistant", Content: correctorRaw})

		corrections := parseCategoryCorrections(correctorRaw)
		applyCategoryCorrections(rawResult, corrections)
	}
	return rawResult
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

func buildCategoryCorrectorMessage(findings []rawDeepFinding, badIdx []int) string {
	validList := ai.AllCategorySlugs()
	var b strings.Builder
	b.WriteString("Some findings you emitted have categories outside the allowed list.\n\n")
	b.WriteString("For each indexed finding below, either:\n")
	b.WriteString("  (a) re-emit with a `category` chosen from the allowed list. If you change the category, also provide a `subcategory` that belongs to that category (or leave it empty).\n")
	b.WriteString("  (b) withdraw the finding by setting `withdraw: true` and giving a one-line `reason`.\n\n")
	b.WriteString("Allowed categories:\n")
	for _, s := range validList {
		b.WriteString("  - " + s + "\n")
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
