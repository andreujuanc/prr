package audit

import (
	"context"
	"fmt"
	"os"

	"github.com/andreujuanc/prr/internal/ai"
)

// RunPlain executes the audit without the Bubble Tea UI — prints progress to stderr.
// Used in --debug mode (debug output would be clobbered by terminal animations)
// and in --quiet mode (no UI wanted at all). When quiet is true the progress
// lines and the header are suppressed so output piped to a log file stays
// machine-readable.
func RunPlain(
	ctx context.Context,
	reviewClient ai.Client,
	aoiClient ai.Client,
	opts Options,
	reviewModel, aoiModel string,
	noSynthesis bool,
	quiet bool,
) (*Result, *SynthesisResult, error) {
	onProgress := func(phase, msg string) {
		if quiet {
			return
		}
		fmt.Fprintf(os.Stderr, "[%s] %s\n", phase, msg)
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "\n  review: %s  aoi: %s\n\n", reviewModel, aoiModel)
	}

	result, err := Run(ctx, reviewClient, aoiClient, opts, onProgress)
	if err != nil {
		return nil, nil, err
	}

	// Run synthesis unless disabled
	var synthesis *SynthesisResult
	if !noSynthesis && result != nil && len(result.Findings) > 0 {
		onProgress("synthesis", "Generating executive summary...")
		synthesis, err = SynthesizeCached(ctx, reviewClient, result.Findings, result.CrossCuttingObservations, result.ProjectContext, len(result.FailedAOIIDs), nil, opts.NoCache)
		if err != nil && !quiet {
			fmt.Fprintf(os.Stderr, "Warning: synthesis failed: %v\n", err)
		}
		result.Usage.Synth = ai.SnapshotUsage(reviewClient)
	}

	return result, synthesis, nil
}
