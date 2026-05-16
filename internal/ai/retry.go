package ai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
	"time"
)

// ── Transient error classification ──────────────────────────────────────
//
// "Transient" means: the call may succeed on retry — rate limit, 5xx,
// network blip, per-call deadline. The classifier takes parentCtx so
// it can correctly distinguish a per-call timeout (transient: parent
// alive, retry with fresh budget) from a parent cancel or parent
// deadline (terminal: caller is done waiting).
//
// Without the parentCtx check, treating context.DeadlineExceeded as
// transient would loop forever on a user Esc; treating it as terminal
// would abort the whole pipeline on a single per-call hiccup. Taking
// parentCtx makes the distinction clean.

var transientStatusCodeRe = regexp.MustCompile(`\b(429|500|502|503|504)\b`)

// transientPhraseRe matches phrase-level signals of transient failure.
// All matches are lowercase; the haystack is lowercased before checking.
// EOF is bounded on both sides so it doesn't match inside words like
// "endpointoflist".
var transientPhraseRe = regexp.MustCompile(
	`rate limit|timeout|temporary failure|connection reset|\beof\b`,
)

// IsTransientError reports whether err is the kind that may succeed
// on retry, given that parentCtx is still alive. Parent cancellation
// or parent deadline always short-circuits to false — the caller has
// already given up.
//
// Per-call timeouts: when a provider wraps ctx with context.WithTimeout
// (RequestTimeout pattern) and that child deadline fires, the error
// wraps context.DeadlineExceeded but parentCtx is still alive. That
// case is transient — retrying gets a fresh per-call budget.
//
// Explicit context.Canceled is treated as terminal even when
// parentCtx looks alive, because Canceled propagates: if any ctx in
// the chain was canceled, parent.Err() returns Canceled too.
func IsTransientError(err error, parentCtx context.Context) bool {
	if err == nil {
		return false
	}
	// Parent done → terminal regardless of err.
	if parentCtx != nil && parentCtx.Err() != nil {
		return false
	}
	// Canceled is terminal (user/ancestor cancel).
	if errors.Is(err, context.Canceled) {
		return false
	}
	// Per-call deadline with parent alive: retry.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Wrapped io.EOF — common when an upstream stream closes unexpectedly.
	if errors.Is(err, io.EOF) {
		return true
	}
	s := strings.ToLower(err.Error())
	if transientStatusCodeRe.MatchString(s) {
		return true
	}
	if transientPhraseRe.MatchString(s) {
		return true
	}
	return false
}

// TransientBackoff returns a quadratic backoff for retry attempt n
// (1-indexed): 1s, 4s, 9s, 16s … Tuned so the first retry is fast
// (most transient blips clear in < 2s) but subsequent retries give
// real backpressure (rate-limit windows on Gemini/OpenAI are 30–60s).
func TransientBackoff(attempt int) time.Duration {
	return time.Duration(attempt*attempt) * time.Second
}

// RetryTransient calls fn up to maxAttempts times, retrying when
// IsTransientError(err, parentCtx) returns true. Each retry waits
// TransientBackoff(attempt) before the next call, respecting
// parentCtx cancellation.
//
// On terminal error (parent canceled, non-transient error, or
// exhausted attempts), returns the last error. On success at any
// attempt, returns the value immediately.
//
// fn receives the parent ctx — providers wrap with their own
// RequestTimeout internally, so the parent ctx represents the
// caller's full budget. A retried call gets a fresh per-call
// budget naturally.
//
// label is a short identifier for log messages ("recheck-dismiss",
// "audit-synthesis", etc.) — purely diagnostic.
func RetryTransient[T any](
	parentCtx context.Context,
	maxAttempts int,
	label string,
	fn func(ctx context.Context) (T, error),
) (T, error) {
	var zero T
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := parentCtx.Err(); err != nil {
			return zero, err
		}
		result, err := fn(parentCtx)
		if err == nil {
			return result, nil
		}
		lastErr = err

		if !IsTransientError(err, parentCtx) {
			return zero, err
		}
		if attempt >= maxAttempts {
			break
		}
		backoff := TransientBackoff(attempt)
		log.Printf("%s: transient error on attempt %d/%d (%v); retrying in %v",
			label, attempt, maxAttempts, err, backoff)
		select {
		case <-time.After(backoff):
		case <-parentCtx.Done():
			return zero, parentCtx.Err()
		}
	}
	return zero, fmt.Errorf("%s: exhausted %d attempts: %w", label, maxAttempts, lastErr)
}
