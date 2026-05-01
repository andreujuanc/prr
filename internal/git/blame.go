package git

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// BlameLine holds blame information for a single source line.
type BlameLine struct {
	Author string
	Date   string // YYYY-MM-DD
}

// BlameFile runs git blame on a file at a specific ref and returns
// blame info keyed by line number (1-indexed).
func BlameFile(ctx context.Context, ref, filePath string) (map[int]BlameLine, error) {
	// Use porcelain format for reliable parsing
	cmd := exec.CommandContext(ctx, "git", "blame", "--porcelain", ref, "--", filePath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git blame failed: %w\n%s", err, stderr.String())
	}

	return parsePorcelainBlame(stdout.String()), nil
}

// parsePorcelainBlame parses git blame --porcelain output into BlameLine entries.
//
// In porcelain format, the first time a commit appears it includes full
// metadata (author, author-time, etc.). Subsequent lines from the same
// commit only have the short SHA header. We cache metadata by SHA so
// every line gets blame info.
func parsePorcelainBlame(output string) map[int]BlameLine {
	result := make(map[int]BlameLine)
	lines := strings.Split(output, "\n")

	// Cache commit metadata by SHA
	type commitInfo struct {
		author string
		date   string
	}
	commitCache := make(map[string]commitInfo)

	var currentLine int
	var currentSHA string
	var author, date string

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		// Header line: <sha1> <orig-line> <final-line> [<num-lines>]
		// SHA1 is always exactly 40 hex characters followed by a space.
		if len(line) >= 42 && line[40] == ' ' && isAllHex(line[:40]) {
			parts := strings.Fields(line)
			currentSHA = parts[0]
			if len(parts) >= 3 {
				if n, err := strconv.Atoi(parts[2]); err == nil {
					currentLine = n
				}
			}
			// Pre-fill from cache for continuation lines
			if ci, ok := commitCache[currentSHA]; ok {
				author = ci.author
				date = ci.date
			} else {
				author = ""
				date = ""
			}
			continue
		}

		if strings.HasPrefix(line, "author ") {
			author = strings.TrimPrefix(line, "author ")
		} else if strings.HasPrefix(line, "author-time ") {
			ts := strings.TrimPrefix(line, "author-time ")
			if epoch, err := strconv.ParseInt(ts, 10, 64); err == nil {
				date = time.Unix(epoch, 0).Format("2006-01-02")
			}
		} else if len(line) > 0 && line[0] == '\t' {
			// Content line — marks end of metadata for this line
			if currentLine > 0 && author != "" {
				// Cache this commit's metadata for future continuation lines
				if currentSHA != "" {
					commitCache[currentSHA] = commitInfo{author: author, date: date}
				}
				result[currentLine] = BlameLine{
					Author: truncateAuthor(author),
					Date:   date,
				}
			}
		}
	}

	return result
}

// isAllHex returns true if every byte in the string is a hexadecimal character.
func isAllHex(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

// truncateAuthor shortens an author name to at most 15 characters.
func truncateAuthor(name string) string {
	runes := []rune(name)
	if len(runes) <= 15 {
		return name
	}
	return string(runes[:14]) + "\u2026"
}
