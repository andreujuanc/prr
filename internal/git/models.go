package git

// PRFile represents a file changed in the pull request
type PRFile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// PullRequest represents the metadata of a pull request
// fetched from the GitHub CLI.
type PullRequest struct {
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	BaseRefName string   `json:"baseRefName"`
	HeadRefName string   `json:"headRefName"`
	HeadRefOid  string   `json:"headRefOid"`
	Files       []PRFile `json:"files"`
}
