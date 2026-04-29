package state

// SyncWithDiffs compares current diff hashes against stored hashes and invalidates
// state where necessary.
// currentDiffHashes is a map of file path to the SHA-256 hash of its current diff.
func (s *State) SyncWithDiffs(currentDiffHashes map[string]string) {
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
			// File diff has changed since last review
			fileState.Status = StatusUnreviewed
			fileState.DiffHash = currentHash
			// Clear specific chat history for this file since the code has changed
			fileState.Chat = nil
			anyFileChanged = true
		}
	}

	// Clean up files that are no longer in the PR
	for path := range s.Files {
		if _, exists := currentDiffHashes[path]; !exists {
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
