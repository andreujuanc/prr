package git

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashDiff generates a SHA-256 hash of the provided diff string.
// This is used for invalidating locally cached state if the diff changes.
func HashDiff(diff string) string {
	hasher := sha256.New()
	hasher.Write([]byte(diff))
	return hex.EncodeToString(hasher.Sum(nil))
}
