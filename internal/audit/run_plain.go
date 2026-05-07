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

	result, err := Run(ctx, reviewClient, aoiClient, opts, onProgress)
	if err != nil {
		return nil, nil, err
	}

	// Snapshot usage before synthesis
	snapshotUsage := func(client ai.Client) ai.TokenUsage {
		if ur, ok := client.(ai.UsageReporter); ok {
			u := ur.Usage()
			ur.ResetUsage()
			return u
		}
		return ai.TokenUsage{}
	}

	// Run synthesis unless disabled
	var synthesis *SynthesisResult
	if !noSynthesis && result != nil && len(result.Findings) > 0 {
		onProgress("synthesis", "Generating executive summary...")
		synthesis, err = Synthesize(ctx, reviewClient, result.Findings, result.CrossCuttingObservations, "", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: synthesis failed: %v\n", err)
		}
		result.Usage.Synth = snapshotUsage(reviewClient)
	}

	return result, synthesis, nil
}
