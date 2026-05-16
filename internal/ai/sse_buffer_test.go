package ai

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGeminiParseSSE_LineOverflowSurfacesEventError pins that an SSE
// chunk larger than sseBufferMax produces an EventError carrying
// bufio.ErrTooLong — rather than a silent EventDone with truncated
// content.
//
// We construct a chunk just past the 8MB cap by stuffing a giant text
// field into one geminiStreamResponse.
func TestGeminiParseSSE_LineOverflowSurfacesEventError(t *testing.T) {
	bigText := strings.Repeat("A", sseBufferMax+1024)
	body := `data: {"candidates":[{"content":{"parts":[{"text":"` + bigText + `"}]}}]}` + "\n\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	provider := newTestProvider(srv.URL)

	ch, err := provider.StreamChat(context.Background(), ChatRequest{
		Messages: []ProviderMessage{
			{Role: RoleUser, Content: []ContentBlock{TextBlock{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}

	var sawError, sawDone bool
	var streamErr error
	for ev := range ch {
		switch ev.Type {
		case EventError:
			sawError = true
			streamErr = ev.Err
		case EventDone:
			sawDone = true
		}
	}

	if sawDone {
		t.Error("expected no EventDone when stream overflows the buffer")
	}
	if !sawError {
		t.Fatal("expected EventError for oversized SSE line")
	}
	if !strings.Contains(streamErr.Error(), "cap") {
		t.Errorf("error should mention the cap, got: %v", streamErr)
	}
	if !errors.Is(streamErr, bufio.ErrTooLong) {
		t.Errorf("expected wrapped bufio.ErrTooLong, got: %v", streamErr)
	}
}
