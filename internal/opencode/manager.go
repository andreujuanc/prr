package opencode

import (
	"context"
	"fmt"
	"sync"
)

// Manager is the top-level coordinator for the OpenCode integration.
// It manages the server lifecycle, shared SSE stream, and per-session routing.
type Manager struct {
	mu     sync.Mutex
	server *Server
	stream *EventStream
	ctx    context.Context
	cancel context.CancelFunc
	dir    string
}

// NewManager creates a Manager that will run the server in the given directory.
func NewManager(dir string) *Manager {
	return &Manager{
		dir: dir,
	}
}

// Start launches the server and connects the SSE event stream.
// Safe to call multiple times (idempotent).
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.server != nil && m.server.Status() == ServerConnected {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	mgrCtx, cancel := context.WithCancel(ctx)

	server := NewServer(m.dir)
	if err := server.Start(mgrCtx); err != nil {
		cancel()
		return fmt.Errorf("start opencode server: %w", err)
	}

	client := server.Client()
	stream, err := NewEventStream(mgrCtx, client)
	if err != nil {
		server.Stop()
		cancel()
		return fmt.Errorf("connect event stream: %w", err)
	}

	m.mu.Lock()
	m.server = server
	m.stream = stream
	m.ctx = mgrCtx
	m.cancel = cancel
	m.mu.Unlock()

	return nil
}

// Stop shuts down the event stream and server.
func (m *Manager) Stop() {
	m.mu.Lock()
	stream := m.stream
	server := m.server
	cancel := m.cancel
	m.stream = nil
	m.server = nil
	m.mu.Unlock()

	if stream != nil {
		stream.Close()
	}
	if server != nil {
		server.Stop()
	}
	if cancel != nil {
		cancel()
	}
}

// Status returns the server connection status.
func (m *Manager) Status() ServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.server == nil {
		return ServerDisconnected
	}
	return m.server.Status()
}

// Client returns the HTTP client for the running server.
func (m *Manager) Client() *Client {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.server == nil {
		return nil
	}
	return m.server.Client()
}

// Stream returns the shared SSE event stream.
func (m *Manager) Stream() *EventStream {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stream
}

// Context returns the manager's context (valid while running).
func (m *Manager) Context() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ctx
}
