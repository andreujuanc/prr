package progress

import (
	"fmt"
	"strings"
	"time"
)

// ParseBatchEvent updates s.Batches from a (phase, message) pair when
// the message matches one of the per-batch lifecycle shapes emitted by
// the review and audit pipelines:
//
//	Batch K: init label="..." files=N kind=aoi-driven|general
//	Batch K: active
//	Batch K: stream bytes=N
//	Batch K: done|cached|failed
//
// K is the 1-based batch number on the wire; stored as 0-based Index.
// Returns true when the message was a batch event (matched or
// unparseable but recognized prefix), so callers can short-circuit
// their own parsers.
func ParseBatchEvent(s *State, message string) bool {
	if !strings.HasPrefix(message, "Batch ") {
		return false
	}
	// "Batch K: payload"
	rest, ok := strings.CutPrefix(message, "Batch ")
	if !ok {
		return false
	}
	colon := strings.Index(rest, ": ")
	if colon < 0 {
		return false
	}
	var oneBased int
	if _, err := fmt.Sscanf(rest[:colon], "%d", &oneBased); err != nil {
		return false
	}
	if oneBased < 1 {
		return false
	}
	idx := oneBased - 1
	payload := rest[colon+2:]

	if s.Batches == nil {
		s.Batches = make(map[int]*BatchState)
	}
	b := s.Batches[idx]
	if b == nil {
		b = &BatchState{Index: idx, Status: BatchQueued}
		s.Batches[idx] = b
	}

	switch {
	case strings.HasPrefix(payload, "init "):
		// init label="<...>" files=N kind=<...>
		// Label is quoted so labels can contain spaces or brackets.
		init := strings.TrimPrefix(payload, "init ")
		label, rest := scanQuotedLabel(init)
		b.Label = label
		// rest is " files=N kind=..."
		var files int
		var kind string
		if n, _ := fmt.Sscanf(rest, " files=%d kind=%s", &files, &kind); n == 2 {
			b.Files = files
			b.Kind = kind
		}
		if b.Status == "" {
			b.Status = BatchQueued
		}
		return true

	case payload == "active":
		b.Status = BatchActive
		if b.StartedAt.IsZero() {
			b.StartedAt = time.Now()
		}
		return true

	case strings.HasPrefix(payload, "stream bytes="):
		var n int
		if _, err := fmt.Sscanf(payload, "stream bytes=%d", &n); err == nil {
			b.Bytes = n
		}
		return true

	case payload == "done":
		b.Status = BatchDone
		b.EndedAt = time.Now()
		return true

	case payload == "cached":
		b.Status = BatchCached
		// Cached batches skip the active phase and end immediately.
		// Set EndedAt so the recent-completions tail can sort them
		// correctly relative to fresh-done rows.
		if b.EndedAt.IsZero() {
			b.EndedAt = time.Now()
		}
		return true

	case payload == "failed":
		b.Status = BatchFailed
		b.EndedAt = time.Now()
		return true
	}

	return false
}

// scanQuotedLabel reads a "...."-quoted label from the start of s and
// returns (label, rest). Returns ("", s) when no quoted label is
// present. Doesn't try to handle escape sequences — labels are simple
// directory paths and category strings that never contain quotes.
func scanQuotedLabel(s string) (label, rest string) {
	if !strings.HasPrefix(s, `label="`) {
		return "", s
	}
	tail := strings.TrimPrefix(s, `label="`)
	end := strings.Index(tail, `"`)
	if end < 0 {
		return "", s
	}
	return tail[:end], tail[end+1:]
}
