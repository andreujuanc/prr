package state

// SyncWithDiffs compares current diff hashes against stored hashes and invalidates
// state where necessary.
// currentDiffHashes maps file path to SHA-256 hash of its current diff (only files where diff succeeded).
// prFiles is the complete set of files in the PR (used to detect removed files).
func (s *State) SyncWithDiffs(currentDiffHashes map[string]string, prFiles map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	anyFileChanged := false

	for path, currentHash := range currentDiffHashes {
		fileState, exists := s.Files[path]
		
		if !exists {
			// New file added to the PR
			s.Files[path] = &FileState{
				Status:   StatusUnreviewed,
				DiffHash: currentHash,
			}
			anyFileChanged = true
			continue
		}

		if fileState.DiffHash != currentHash {
			// File diff has changed since last review — mark as modified if previously reviewed
			if fileState.Status == StatusReviewed {
				fileState.Status = StatusModified
			} else {
				fileState.Status = StatusUnreviewed
			}
			fileState.DiffHash = currentHash
			// Clear specific chat history and cached batch findings for this file since the code has changed
			fileState.Chat = nil
			fileState.BatchFindings = ""
			fileState.Purpose = ""
			anyFileChanged = true
		}
	}

	// Clean up files that are no longer in the PR (use the full PR file list,
	// not the hash map, to avoid deleting files where GetRawDiff failed transiently)
	for path := range s.Files {
		if !prFiles[path] {
			delete(s.Files, path)
			anyFileChanged = true
		}
	}

	// If any file changed, clear the global chat because the overall context
	// of the PR has likely shifted.
	if anyFileChanged {
		s.GlobalChat = nil
	}
}

// DiffSnapshotFromFiles returns a snapshot of the current DiffHash for each
// file in the state. This is meant to be stored on AIReview.DiffSnapshot at
// the time a review is generated so staleness can be detected later.
func (s *State) DiffSnapshotFromFiles() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap := make(map[string]string, len(s.Files))
	for path, fs := range s.Files {
		snap[path] = fs.DiffHash
	}
	return snap
}

// IsReviewStale reports whether the stored review's DiffSnapshot differs from
// the current file diff hashes. A review is considered stale when any file's
// hash has changed, files have been added, or files have been removed since
// the review was generated.
func (s *State) IsReviewStale() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.Review == nil || s.Review.DiffSnapshot == nil {
		return false // no snapshot → can't determine staleness
	}

	snap := s.Review.DiffSnapshot

	// Check for files changed or added since the review
	for path, fs := range s.Files {
		oldHash, ok := snap[path]
		if !ok || oldHash != fs.DiffHash {
			return true
		}
	}

	// Check for files removed since the review
	for path := range snap {
		if _, ok := s.Files[path]; !ok {
			return true
		}
	}

	return false
}
