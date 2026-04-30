package git

// PRFile represents a file changed in the pull request
type PRFile struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// PRAuthor represents the author of a pull request.
type PRAuthor struct {
	Login string `json:"login"`
}

// PRRepo represents a repository reference from the GitHub CLI.
type PRRepo struct {
	Name  string  `json:"name"`
	Owner PROwner `json:"owner"`
}

// PROwner represents the owner of a repository.
type PROwner struct {
	Login string `json:"login"`
}

// PullRequest represents the metadata of a pull request
// fetched from the GitHub CLI.
type PullRequest struct {
	Number         int      `json:"number"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	State          string   `json:"state"`
	BaseRefName    string   `json:"baseRefName"`
	HeadRefName    string   `json:"headRefName"`
	HeadRefOid     string   `json:"headRefOid"`
	Author         PRAuthor `json:"author"`
	HeadRepository PRRepo   `json:"headRepository"`
	ReviewDecision string   `json:"reviewDecision"`
	Files          []PRFile `json:"files"`
}
