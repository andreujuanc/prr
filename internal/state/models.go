package state

import (
	"sync"
)

// ReviewStatus represents the current review state of a file
type ReviewStatus string

const (
	StatusUnreviewed ReviewStatus = "unreviewed"
	StatusReviewed   ReviewStatus = "reviewed"
	StatusModified   ReviewStatus = "modified" // Represents a file that was reviewed but has new changes
)

// Message represents a single chat message in an AI conversation
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// FileState holds the review status and chat history for a specific file
type FileState struct {
	Status   ReviewStatus `json:"status"`
	DiffHash string       `json:"diff_hash"`
	Chat     []Message    `json:"chat,omitempty"`
}

// State represents the persisted review state for a single pull request
type State struct {
	mu sync.RWMutex

	PRNumber   string               `json:"pr_number"`
	GlobalChat []Message            `json:"global_chat,omitempty"`
	Files      map[string]*FileState `json:"files"`
}

// NewState initializes a new empty state object for a PR
func NewState(prNumber string) *State {
	return &State{
		PRNumber: prNumber,
		Files:    make(map[string]*FileState),
	}
}
