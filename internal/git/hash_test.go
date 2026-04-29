package git

import (
	"encoding/json"
	"testing"
)

func TestHashDiff(t *testing.T) {
	diff1 := "--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+new"
	diff2 := "--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-old\n+newer"
	
	hash1 := HashDiff(diff1)
	hash2 := HashDiff(diff2)
	
	if hash1 == "" {
		t.Errorf("HashDiff returned empty string")
	}
	
	if hash1 == hash2 {
		t.Errorf("HashDiff returned identical hashes for different diffs: %s", hash1)
	}
	
	// Ensure determinism
	if HashDiff(diff1) != hash1 {
		t.Errorf("HashDiff is not deterministic")
	}
}

func TestPullRequestJSONUnmarshaling(t *testing.T) {
	jsonData := `{
		"number": 123,
		"title": "Fix a bug",
		"baseRefName": "main",
		"headRefName": "fix-bug",
		"headRefOid": "abcd123",
		"files": [
			{
				"path": "file1.txt",
				"additions": 10,
				"deletions": 5
			}
		]
	}`

	var pr PullRequest
	if err := json.Unmarshal([]byte(jsonData), &pr); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	if pr.Number != 123 {
		t.Errorf("Expected PR number 123, got %d", pr.Number)
	}
	if pr.Title != "Fix a bug" {
		t.Errorf("Expected PR title 'Fix a bug', got '%s'", pr.Title)
	}
	if len(pr.Files) != 1 {
		t.Fatalf("Expected 1 file, got %d", len(pr.Files))
	}
	if pr.Files[0].Path != "file1.txt" {
		t.Errorf("Expected file path 'file1.txt', got '%s'", pr.Files[0].Path)
	}
}
