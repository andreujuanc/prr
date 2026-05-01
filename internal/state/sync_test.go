package state

import (
	"testing"
)

func TestDiffSnapshotFromFiles(t *testing.T) {
	s := NewState("42")
	s.Files["a.go"] = &FileState{DiffHash: "hash-a"}
	s.Files["b.go"] = &FileState{DiffHash: "hash-b"}

	snap := s.DiffSnapshotFromFiles()

	if len(snap) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(snap))
	}
	if snap["a.go"] != "hash-a" {
		t.Errorf("a.go: got %q, want %q", snap["a.go"], "hash-a")
	}
	if snap["b.go"] != "hash-b" {
		t.Errorf("b.go: got %q, want %q", snap["b.go"], "hash-b")
	}
}

func TestIsReviewStale(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(s *State)
		wantStale bool
	}{
		{
			name: "no review — not stale",
			setup: func(s *State) {
				s.Files["a.go"] = &FileState{DiffHash: "hash-a"}
			},
			wantStale: false,
		},
		{
			name: "review without snapshot — not stale",
			setup: func(s *State) {
				s.Files["a.go"] = &FileState{DiffHash: "hash-a"}
				s.Review = &AIReview{Summary: "looks good"}
			},
			wantStale: false,
		},
		{
			name: "matching snapshot — not stale",
			setup: func(s *State) {
				s.Files["a.go"] = &FileState{DiffHash: "hash-a"}
				s.Files["b.go"] = &FileState{DiffHash: "hash-b"}
				s.Review = &AIReview{
					Summary: "looks good",
					DiffSnapshot: map[string]string{
						"a.go": "hash-a",
						"b.go": "hash-b",
					},
				}
			},
			wantStale: false,
		},
		{
			name: "file hash changed — stale",
			setup: func(s *State) {
				s.Files["a.go"] = &FileState{DiffHash: "hash-a-v2"}
				s.Files["b.go"] = &FileState{DiffHash: "hash-b"}
				s.Review = &AIReview{
					Summary: "looks good",
					DiffSnapshot: map[string]string{
						"a.go": "hash-a",
						"b.go": "hash-b",
					},
				}
			},
			wantStale: true,
		},
		{
			name: "new file added — stale",
			setup: func(s *State) {
				s.Files["a.go"] = &FileState{DiffHash: "hash-a"}
				s.Files["c.go"] = &FileState{DiffHash: "hash-c"}
				s.Review = &AIReview{
					Summary: "looks good",
					DiffSnapshot: map[string]string{
						"a.go": "hash-a",
					},
				}
			},
			wantStale: true,
		},
		{
			name: "file removed — stale",
			setup: func(s *State) {
				s.Files["a.go"] = &FileState{DiffHash: "hash-a"}
				s.Review = &AIReview{
					Summary: "looks good",
					DiffSnapshot: map[string]string{
						"a.go": "hash-a",
						"b.go": "hash-b",
					},
				}
			},
			wantStale: true,
		},
		{
			name: "empty snapshot with files — stale",
			setup: func(s *State) {
				s.Files["a.go"] = &FileState{DiffHash: "hash-a"}
				s.Review = &AIReview{
					Summary:      "looks good",
					DiffSnapshot: map[string]string{},
				}
			},
			wantStale: true,
		},
		{
			name: "empty files with snapshot — stale",
			setup: func(s *State) {
				s.Review = &AIReview{
					Summary: "looks good",
					DiffSnapshot: map[string]string{
						"a.go": "hash-a",
					},
				}
			},
			wantStale: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewState("42")
			tt.setup(s)
			got := s.IsReviewStale()
			if got != tt.wantStale {
				t.Errorf("IsReviewStale() = %v, want %v", got, tt.wantStale)
			}
		})
	}
}
