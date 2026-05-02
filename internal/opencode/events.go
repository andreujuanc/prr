package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// EventStream manages an SSE connection and broadcasts events to subscribers.
type EventStream struct {
	client *Client
	ctx    context.Context
	cancel context.CancelFunc
	body   io.ReadCloser

	mu          sync.Mutex
	subscribers map[*subscriber]struct{}
}

// subscriber is an internal channel + context for a single listener.
type subscriber struct {
	ch     chan Event
	ctx    context.Context
	cancel context.CancelFunc
}

// NewEventStream connects to the SSE endpoint and begins parsing events.
// Events are broadcast to all subscribers. Call Close() to stop.
func NewEventStream(ctx context.Context, client *Client) (*EventStream, error) {
	streamCtx, cancel := context.WithCancel(ctx)

	body, err := client.ConnectEvents(streamCtx)
	if err != nil {
		cancel()
		return nil, err
	}

	es := &EventStream{
		client:      client,
		ctx:         streamCtx,
		cancel:      cancel,
		body:        body,
		subscribers: make(map[*subscriber]struct{}),
	}

	go es.readLoop()
	return es, nil
}

// Subscribe creates a new event channel filtered for the given session ID.
// Pass "" for sessionID to receive all events.
// The returned channel is closed when the stream closes or Unsubscribe is called.
func (es *EventStream) Subscribe(ctx context.Context, sessionID string) (<-chan Event, func()) {
	subCtx, subCancel := context.WithCancel(ctx)
	sub := &subscriber{
		ch:     make(chan Event, 32),
		ctx:    subCtx,
		cancel: subCancel,
	}

	es.mu.Lock()
	es.subscribers[sub] = struct{}{}
	es.mu.Unlock()

	// Wrap in a filtered channel if sessionID is specified
	if sessionID == "" {
		unsubscribe := func() {
			es.removeSub(sub)
			subCancel()
		}
		return sub.ch, unsubscribe
	}

	// Create filtered channel
	filtered := make(chan Event, 32)
	go func() {
		defer close(filtered)
		for {
			select {
			case <-subCtx.Done():
				return
			case event, ok := <-sub.ch:
				if !ok {
					return
				}
				if event.Properties.SessionID == sessionID || event.Properties.SessionID == "" {
					select {
					case filtered <- event:
					case <-subCtx.Done():
						return
					}
				}
			}
		}
	}()

	unsubscribe := func() {
		es.removeSub(sub)
		subCancel()
	}
	return filtered, unsubscribe
}

// removeSub removes a subscriber from the broadcast list and closes its channel.
func (es *EventStream) removeSub(sub *subscriber) {
	es.mu.Lock()
	if _, exists := es.subscribers[sub]; exists {
		delete(es.subscribers, sub)
		close(sub.ch)
	}
	es.mu.Unlock()
}

// Close shuts down the event stream and all subscribers.
func (es *EventStream) Close() {
	es.cancel()
	if es.body != nil {
		es.body.Close()
	}
}

// readLoop reads lines from the SSE stream and broadcasts parsed events.
func (es *EventStream) readLoop() {
	defer es.closeAllSubs()

	scanner := bufio.NewScanner(es.body)
	// SSE messages can be large (tool output, etc.)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-es.ctx.Done():
			return
		default:
		}

		line := scanner.Text()

		// SSE format: "data: {...json...}"
		if !strings.HasPrefix(line, "data: ") {
			continue // skip empty lines, comments, etc.
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "" {
			continue
		}

		event, err := parseEvent([]byte(data))
		if err != nil {
			// Non-fatal: skip malformed events
			continue
		}

		es.broadcast(event)
	}
}

// broadcast sends an event to all subscribers (non-blocking).
func (es *EventStream) broadcast(event Event) {
	es.mu.Lock()
	defer es.mu.Unlock()

	for sub := range es.subscribers {
		select {
		case sub.ch <- event:
		case <-sub.ctx.Done():
			// Subscriber gone — will be cleaned up
		default:
			// Channel full — drop event to avoid blocking
		}
	}
}

// closeAllSubs closes all subscriber channels (called when stream ends).
func (es *EventStream) closeAllSubs() {
	es.mu.Lock()
	defer es.mu.Unlock()

	for sub := range es.subscribers {
		close(sub.ch)
		sub.cancel()
	}
	es.subscribers = make(map[*subscriber]struct{})
}

// ── Event parsing ───────────────────────────────────────────────────────

// parseEvent parses a raw JSON SSE data payload into an Event.
func parseEvent(data []byte) (Event, error) {
	// First extract type and properties as raw JSON
	var envelope struct {
		Type       EventType       `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return Event{}, fmt.Errorf("parse event envelope: %w", err)
	}

	event := Event{
		Type: envelope.Type,
		Raw:  data,
	}

	// Parse properties based on event type
	if envelope.Properties != nil {
		props, err := parseProperties(envelope.Type, envelope.Properties)
		if err != nil {
			return Event{}, fmt.Errorf("parse properties for %s: %w", envelope.Type, err)
		}
		event.Properties = props
	}

	return event, nil
}

// parseProperties decodes the properties JSON according to the event type.
func parseProperties(eventType EventType, raw json.RawMessage) (EventProperties, error) {
	var props EventProperties

	// Decode common fields first
	if err := json.Unmarshal(raw, &props); err != nil {
		return props, err
	}

	// For events that use "info" field, decode into the correct type
	switch eventType {
	case EventSessionUpdated:
		var wrapper struct {
			Info *Session `json:"info"`
		}
		if err := json.Unmarshal(raw, &wrapper); err == nil {
			props.Session = wrapper.Info
		}

	case EventMessageUpdated:
		var wrapper struct {
			Info *MessageInfo `json:"info"`
		}
		if err := json.Unmarshal(raw, &wrapper); err == nil {
			props.MessageInfo = wrapper.Info
		}

	case EventPermissionAsked:
		// permission.asked properties contain the permission directly
		var perm Permission
		if err := json.Unmarshal(raw, &perm); err == nil {
			props.Permission = &perm
			props.SessionID = perm.SessionID
		}

	case EventQuestionAsked:
		// question.asked properties contain the question directly
		var q Question
		if err := json.Unmarshal(raw, &q); err == nil {
			props.Question = &q
			props.SessionID = q.SessionID
		}
	}

	return props, nil
}
