package audit

import (
	"context"
	"fmt"
	"os"

	"github.com/andreujuanc/prr/internal/ai"
)

// RunPlain executes the audit without the Bubble Tea UI — prints progress to stderr.
// Used in --debug mode so debug output isn't clobbered by terminal animations.
func RunPlain(
	ctx context.Context,
	reviewClient ai.Client,
	aoiClient ai.Client,
	opts Options,
	reviewModel, aoiModel string,
	noSynthesis bool,
) (*Result, *SynthesisResult, error) {
	onProgress := func(phase, msg string) {
		fmt.Fprintf(os.Stderr, "[%s] %s\n", phase, msg)
	}

	fmt.Fprintf(os.Stderr, "\n  review: %s  aoi: %s\n\n", reviewModel, aoiModel)

	result, err := Run(ctx, reviewClient, aoiClient, opts, onProgress)
	if err != nil {
		return nil, nil, err
	}

	// Run synthesis unless disabled
	var synthesis *SynthesisResult
	if !noSynthesis && result != nil && len(result.Findings) > 0 {
		onProgress("synthesis", "Generating executive summary...")
		synthesis, err = Synthesize(ctx, reviewClient, result.Findings, result.CrossCuttingObservations, result.ProjectContext, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: synthesis failed: %v\n", err)
		}
		result.Usage.Synth = ai.SnapshotUsage(reviewClient)
	}

	return result, synthesis, nil
}
