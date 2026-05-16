package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
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

// TransientError marks an error that should be retried and may carry a
// server-provided retry hint. Providers return this for 429 / 5xx so
// the outer RetryTransient wrapper can honour the upstream's preferred
// wait — Gemini's body-embedded retryDelay, OpenAI's Retry-After
// header, etc. — instead of always falling back to the quadratic curve.
//
// RetryAfter == 0 means "no hint, use the default backoff". A non-zero
// value is capped at 60s by the consumer (TransientSleep) to bound
// worst-case latency on misconfigured servers.
type TransientError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *TransientError) Error() string {
	if e == nil || e.Err == nil {
		return "transient error"
	}
	return e.Err.Error()
}

func (e *TransientError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

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
	// Typed TransientError from a provider — always transient.
	var te *TransientError
	if errors.As(err, &te) {
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

// maxRetryAfter caps a server-supplied retry hint. A misbehaving
// server could otherwise stall the pipeline for hours.
const maxRetryAfter = 60 * time.Second

// transientSleep returns the wait before the next retry. When err
// carries a server hint via TransientError.RetryAfter, that wins
// (clamped to [100ms, 60s]); otherwise fall through to the quadratic
// curve.
func transientSleep(err error, attempt int) time.Duration {
	var te *TransientError
	if errors.As(err, &te) && te.RetryAfter > 0 {
		d := te.RetryAfter
		if d < 100*time.Millisecond {
			d = 100 * time.Millisecond
		}
		if d > maxRetryAfter {
			d = maxRetryAfter
		}
		return d
	}
	return TransientBackoff(attempt)
}

// RetryTransient calls fn up to maxAttempts times, retrying when
// IsTransientError(err, parentCtx) returns true. Each retry waits
// transientSleep(err, attempt) before the next call, respecting
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
		backoff := transientSleep(err, attempt)
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

// ── Retry-After parsing ─────────────────────────────────────────────────

// parseGeminiRetryDelay extracts the retryDelay from a Gemini error
// response body. Returns 0 when no hint is present.
//
// Gemini's 429 body shape:
//
//	{"error":{"details":[{"@type":"...RetryInfo","retryDelay":"41s"}]}}
//
// The value is "<seconds>s" — possibly fractional ("41.5s").
func parseGeminiRetryDelay(body []byte) time.Duration {
	var errResp struct {
		Error struct {
			Details []struct {
				Type       string `json:"@type"`
				RetryDelay string `json:"retryDelay"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err != nil {
		return 0
	}
	for _, d := range errResp.Error.Details {
		if d.RetryDelay == "" {
			continue
		}
		v := strings.TrimSuffix(d.RetryDelay, "s")
		secs, err := strconv.ParseFloat(v, 64)
		if err != nil {
			continue
		}
		if secs <= 0 {
			return 100 * time.Millisecond
		}
		return time.Duration(secs * float64(time.Second))
	}
	return 0
}

// parseHTTPRetryAfter extracts a wait duration from a Retry-After
// header. RFC 7231 allows either a decimal number of seconds or an
// HTTP-date (RFC 1123 / RFC 850 / ANSI C asctime). Returns 0 when
// the header is absent or unparseable.
func parseHTTPRetryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs <= 0 {
			return 100 * time.Millisecond
		}
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return 100 * time.Millisecond
		}
		return d
	}
	return 0
}
