package opencode

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// Server manages the lifecycle of an `opencode serve` process.
type Server struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	port    int
	dir     string
	client  *Client
	status  ServerStatus
	cancel  context.CancelFunc
	started bool
}

// NewServer creates a Server that will run in the given directory.
func NewServer(dir string) *Server {
	return &Server{
		dir:    dir,
		status: ServerDisconnected,
	}
}

// Status returns the current server connection status.
func (s *Server) Status() ServerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Client returns the HTTP client for the running server, or nil if not connected.
func (s *Server) Client() *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

// Port returns the port the server is listening on (0 if not started).
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// Start launches the opencode server process and waits until it is healthy.
// It picks a free port automatically. Returns an error if the server fails to start.
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil // already running
	}
	s.status = ServerConnecting
	s.mu.Unlock()

	// Find a free port
	port, err := freePort()
	if err != nil {
		s.mu.Lock()
		s.status = ServerDisconnected
		s.mu.Unlock()
		return fmt.Errorf("find free port: %w", err)
	}

	cmdCtx, cancel := context.WithCancel(ctx)

	cmd := exec.CommandContext(cmdCtx, "opencode", "serve", "--port", strconv.Itoa(port))
	cmd.Dir = s.dir
	// Suppress stdout/stderr — we communicate via HTTP
	cmd.Stdout = nil
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		cancel()
		s.mu.Lock()
		s.status = ServerDisconnected
		s.mu.Unlock()
		return fmt.Errorf("start opencode serve: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.port = port
	s.cancel = cancel
	s.started = true
	s.mu.Unlock()

	// Wait for healthy
	client := NewClient(fmt.Sprintf("http://localhost:%d", port))
	if err := s.waitHealthy(ctx, client); err != nil {
		// Kill process on failure
		cancel()
		_ = cmd.Wait()
		s.mu.Lock()
		s.status = ServerDisconnected
		s.started = false
		s.mu.Unlock()
		return err
	}

	s.mu.Lock()
	s.client = client
	s.status = ServerConnected
	s.mu.Unlock()

	// Monitor process in background
	go s.monitor(cmd, cancel)

	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	cmd := s.cmd
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	// Wait for the process to exit with a timeout; force-kill if needed.
	if cmd != nil && cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			_ = cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}

	s.mu.Lock()
	s.status = ServerDisconnected
	s.started = false
	s.client = nil
	s.mu.Unlock()
}

// waitHealthy polls the health endpoint until the server responds or timeout.
func (s *Server) waitHealthy(ctx context.Context, client *Client) error {
	deadline := time.After(15 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("opencode server did not become healthy within 15s")
		case <-ticker.C:
			h, err := client.Health(ctx)
			if err == nil && h.Healthy {
				return nil
			}
		}
	}
}

// monitor watches for the server process to exit unexpectedly.
func (s *Server) monitor(cmd *exec.Cmd, cancel context.CancelFunc) {
	_ = cmd.Wait()
	s.mu.Lock()
	s.status = ServerDisconnected
	s.started = false
	s.client = nil
	s.mu.Unlock()
	cancel()
}

// freePort finds an available TCP port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port, nil
}
