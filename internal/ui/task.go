package ui

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/andreujuanc/prr/internal/state"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Task status ─────────────────────────────────────────────────────────

// TaskStatus represents the lifecycle state of a background task.
type TaskStatus int

const (
	TaskRunning   TaskStatus = iota // process is executing
	TaskCompleted                   // exited successfully
	TaskFailed                      // exited with error
	TaskCancelled                   // user cancelled
)

// ── Task ────────────────────────────────────────────────────────────────

// Task represents a background "Fix with OpenCode" process.
type Task struct {
	ID         int
	Title      string              // short label e.g. "Fix: null check in auth.go:42"
	FindingIdx int                 // index into m.reviewFindings
	Finding    state.ReviewFinding // snapshot of the finding at spawn time
	Status     TaskStatus
	Error      string // error message if failed
	StartedAt  time.Time

	mu     sync.Mutex
	output strings.Builder // accumulated stdout/stderr
	cancel context.CancelFunc
}

// Output returns the accumulated output (thread-safe).
func (t *Task) Output() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.output.String()
}

// appendOutput appends a line to the output buffer (thread-safe).
func (t *Task) appendOutput(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.output.WriteString(line)
	t.output.WriteByte('\n')
}

// ── Messages ────────────────────────────────────────────────────────────

// TaskSpawnedMsg is sent when a task process has been started.
type TaskSpawnedMsg struct {
	ID int
}

// TaskOutputMsg delivers streaming output lines from a running task.
type TaskOutputMsg struct {
	ID    int
	Lines string // one or more lines of output
}

// TaskDoneMsg is sent when a task process exits.
type TaskDoneMsg struct {
	ID  int
	Err error // nil on success
}

// ── Spawn ───────────────────────────────────────────────────────────────

// spawnOpenCodeTask launches an `opencode run` process in the background.
// It streams output lines back to the TUI via program.Send.
func spawnOpenCodeTask(task *Task, repoRoot string, p *tea.Program) {
	ctx, cancel := context.WithCancel(context.Background())
	task.cancel = cancel

	prompt := buildFixPrompt(task.Finding)

	cmd := exec.CommandContext(ctx, "opencode", "run", "--dir", repoRoot)
	cmd.Stdin = strings.NewReader(prompt)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		task.Status = TaskFailed
		task.Error = fmt.Sprintf("stdout pipe: %v", err)
		p.Send(TaskDoneMsg{ID: task.ID, Err: err})
		return
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout

	if err := cmd.Start(); err != nil {
		task.Status = TaskFailed
		task.Error = fmt.Sprintf("start: %v", err)
		p.Send(TaskDoneMsg{ID: task.ID, Err: err})
		return
	}

	p.Send(TaskSpawnedMsg{ID: task.ID})

	// Stream output lines
	go func() {
		scanner := bufio.NewScanner(stdout)
		// Increase scanner buffer for long lines from opencode
		scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

		var batch strings.Builder
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		lineCh := make(chan string, 64)
		doneCh := make(chan struct{})

		// Reader goroutine
		go func() {
			for scanner.Scan() {
				lineCh <- scanner.Text()
			}
			close(doneCh)
		}()

		flush := func() {
			if batch.Len() > 0 {
				lines := batch.String()
				task.mu.Lock()
				task.output.WriteString(lines)
				task.mu.Unlock()
				p.Send(TaskOutputMsg{ID: task.ID, Lines: lines})
				batch.Reset()
			}
		}

		for {
			select {
			case line, ok := <-lineCh:
				if !ok {
					// Channel closed but doneCh might not have fired yet
					continue
				}
				batch.WriteString(line + "\n")
			case <-ticker.C:
				flush()
			case <-doneCh:
				// Drain remaining
				for {
					select {
					case line := <-lineCh:
						batch.WriteString(line + "\n")
					default:
						goto drained
					}
				}
			drained:
				flush()
				// Wait for process to exit
				waitErr := cmd.Wait()
				if ctx.Err() == context.Canceled {
					task.Status = TaskCancelled
					p.Send(TaskDoneMsg{ID: task.ID, Err: nil})
				} else if waitErr != nil {
					task.Status = TaskFailed
					task.Error = waitErr.Error()
					p.Send(TaskDoneMsg{ID: task.ID, Err: waitErr})
				} else {
					task.Status = TaskCompleted
					p.Send(TaskDoneMsg{ID: task.ID, Err: nil})
				}
				return
			}
		}
	}()
}

// cancelTask sends cancel signal to a running task.
func cancelTask(task *Task) {
	if task.cancel != nil && task.Status == TaskRunning {
		task.cancel()
	}
}

// ── Prompt builder ──────────────────────────────────────────────────────

func buildFixPrompt(f state.ReviewFinding) string {
	var b strings.Builder
	b.WriteString("Fix the following code review finding. The issue is in the file and line indicated below.\n")
	b.WriteString("Do not ask for clarification — just fix it.\n\n")
	b.WriteString(fmt.Sprintf("File: %s:%d\n", f.File, f.Line))
	b.WriteString(fmt.Sprintf("Severity: %s | Category: %s\n", f.Severity, f.Category))
	b.WriteString("\n## Issue\n")
	b.WriteString(f.Title + "\n\n")
	b.WriteString(f.Detail + "\n")
	if f.Suggestion != "" {
		b.WriteString("\n## Suggestion\n")
		b.WriteString(f.Suggestion + "\n")
	}
	return b.String()
}

// ── Helpers ─────────────────────────────────────────────────────────────

// taskTitle builds a short title for a task from its finding.
func taskTitle(f state.ReviewFinding) string {
	title := f.Title
	if len(title) > 40 {
		title = title[:37] + "..."
	}
	return fmt.Sprintf("Fix: %s", title)
}

// hasRunningTaskForFinding checks if there's already a running task for the given finding index.
func hasRunningTaskForFinding(tasks []*Task, findingIdx int) bool {
	for _, t := range tasks {
		if t.FindingIdx == findingIdx && t.Status == TaskRunning {
			return true
		}
	}
	return false
}
