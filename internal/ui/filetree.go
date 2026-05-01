package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/andreujuanc/prr/internal/state"

	"github.com/charmbracelet/x/ansi"
)

// ── File tree types ─────────────────────────────────────────────────────

// treeNode represents a directory or file in the tree.
type treeNode struct {
	name       string // just the segment name (e.g., "model.go")
	path       string // full path (e.g., "internal/ui/model.go"), empty for dirs
	isDir      bool
	isOverview bool // special PR overview item
	children   []*treeNode
	additions  int
	deletions  int
	status     state.ReviewStatus
	expanded   bool
	skipReason string // non-empty if skipped from AI review (e.g. "binary", "generated", "large")
}

// fileTree is a navigable file tree component.
type fileTree struct {
	root         *treeNode
	flat         []flatEntry // flattened visible entries for navigation
	cursor       int
	width        int
	height       int
	offset       int  // scroll offset
	hideReviewed bool // when true, reviewed files are hidden
}

// flatEntry is a single visible row in the tree.
type flatEntry struct {
	node  *treeNode
	depth int
}

// ── Build tree from file list ───────────────────────────────────────────

func newFileTree(files []fileInfo) fileTree {
	root := &treeNode{name: "", isDir: true, expanded: true}

	for _, f := range files {
		parts := strings.Split(f.path, "/")
		current := root
		for i, part := range parts {
			isLast := i == len(parts)-1
			if isLast {
				// File node
				current.children = append(current.children, &treeNode{
					name:       part,
					path:       f.path,
					isDir:      false,
					additions:  f.additions,
					deletions:  f.deletions,
					status:     f.status,
					skipReason: f.skipReason,
				})
			} else {
				// Find or create directory node
				found := false
				for _, child := range current.children {
					if child.isDir && child.name == part {
						current = child
						found = true
						break
					}
				}
				if !found {
					dir := &treeNode{name: part, isDir: true, expanded: true}
					current.children = append(current.children, dir)
					current = dir
				}
			}
		}
	}

	// Sort: dirs first, then alphabetical
	sortTree(root)

	// Collapse single-child directories (e.g., internal/ui -> internal/ui)
	collapseTree(root)

	ft := fileTree{root: root, cursor: 0}
	ft.flatten()
	return ft
}

// fileInfo holds data needed to build the tree.
// fileInfo holds data needed to build the tree.
type fileInfo struct {
	path       string
	additions  int
	deletions  int
	status     state.ReviewStatus
	skipReason string // non-empty if file is skipped from AI review
}

func sortTree(node *treeNode) {
	sort.Slice(node.children, func(i, j int) bool {
		a, b := node.children[i], node.children[j]
		if a.isDir != b.isDir {
			return a.isDir // dirs first
		}
		return a.name < b.name
	})
	for _, child := range node.children {
		if child.isDir {
			sortTree(child)
		}
	}
}

// collapseTree merges single-child directory chains: a/b/c -> a/b/c
func collapseTree(node *treeNode) {
	for i, child := range node.children {
		if child.isDir {
			collapseTree(child)
			// If this dir has exactly one child and it's also a dir, merge
			for len(child.children) == 1 && child.children[0].isDir {
				grandchild := child.children[0]
				child.name = child.name + "/" + grandchild.name
				child.children = grandchild.children
			}
			node.children[i] = child
		}
	}
}

// ── Flatten visible entries ─────────────────────────────────────────────

func (ft *fileTree) flatten() {
	ft.flat = ft.flat[:0]
	// Insert PR Overview as the first item
	ft.flat = append(ft.flat, flatEntry{
		node:  &treeNode{name: "PR Overview", isOverview: true},
		depth: 0,
	})
	ft.flattenNode(ft.root, -1) // root is invisible, children start at depth 0
}

func (ft *fileTree) flattenNode(node *treeNode, depth int) {
	if depth >= 0 {
		// Skip reviewed files when hideReviewed is on
		if ft.hideReviewed && !node.isDir && !node.isOverview && node.status == state.StatusReviewed {
			return
		}
		// Skip dirs that have no visible descendants
		if ft.hideReviewed && node.isDir && !ft.hasVisibleDescendants(node) {
			return
		}
		ft.flat = append(ft.flat, flatEntry{node: node, depth: depth})
	}
	if node.isDir && node.expanded {
		for _, child := range node.children {
			ft.flattenNode(child, depth+1)
		}
	}
}

// hasVisibleDescendants returns true if a dir node contains at least one
// non-reviewed file (recursively).
func (ft *fileTree) hasVisibleDescendants(node *treeNode) bool {
	for _, child := range node.children {
		if child.isDir {
			if ft.hasVisibleDescendants(child) {
				return true
			}
		} else if child.status != state.StatusReviewed {
			return true
		}
	}
	return false
}

// ── Navigation ──────────────────────────────────────────────────────────

func (ft *fileTree) toggleHideReviewed() {
	ft.hideReviewed = !ft.hideReviewed
	ft.flatten()
	if ft.cursor >= len(ft.flat) {
		ft.cursor = len(ft.flat) - 1
	}
	if ft.cursor < 0 {
		ft.cursor = 0
	}
	ft.ensureVisible()
}

func (ft *fileTree) moveUp() {
	if ft.cursor > 0 {
		ft.cursor--
		ft.ensureVisible()
	}
}

func (ft *fileTree) moveDown() {
	if ft.cursor < len(ft.flat)-1 {
		ft.cursor++
		ft.ensureVisible()
	}
}

func (ft *fileTree) toggle() {
	if ft.cursor >= 0 && ft.cursor < len(ft.flat) {
		entry := ft.flat[ft.cursor]
		if entry.node.isDir {
			entry.node.expanded = !entry.node.expanded
			ft.flatten()
			// Clamp cursor
			if ft.cursor >= len(ft.flat) {
				ft.cursor = len(ft.flat) - 1
			}
		}
	}
}

func (ft *fileTree) selectedPath() string {
	if ft.cursor >= 0 && ft.cursor < len(ft.flat) {
		return ft.flat[ft.cursor].node.path
	}
	return ""
}

func (ft *fileTree) selectedIsDir() bool {
	if ft.cursor >= 0 && ft.cursor < len(ft.flat) {
		return ft.flat[ft.cursor].node.isDir && !ft.flat[ft.cursor].node.isOverview
	}
	return false
}

func (ft *fileTree) selectedIsOverview() bool {
	if ft.cursor >= 0 && ft.cursor < len(ft.flat) {
		return ft.flat[ft.cursor].node.isOverview
	}
	return false
}

func (ft *fileTree) ensureVisible() {
	if ft.cursor < ft.offset {
		ft.offset = ft.cursor
	}
	if ft.cursor >= ft.offset+ft.height {
		ft.offset = ft.cursor - ft.height + 1
	}
}

// ── Render ──────────────────────────────────────────────────────────────

func (ft *fileTree) View() string {
	if len(ft.flat) == 0 {
		return styleTextMuted.Render("  No files")
	}

	var lines []string
	end := ft.offset + ft.height
	if end > len(ft.flat) {
		end = len(ft.flat)
	}

	for i := ft.offset; i < end; i++ {
		entry := ft.flat[i]
		isSelected := i == ft.cursor

		var line string
		if entry.node.isOverview {
			icon := styleAccentMauveBold.Render("◆ ")
			if isSelected {
				line = icon + styleAccentBlueBold.Render(entry.node.name)
			} else {
				line = icon + styleAccentMauveBold.Render(entry.node.name)
			}
		} else if entry.node.isDir {
			indent := strings.Repeat("  ", entry.depth)
			icon := "▸ "
			if entry.node.expanded {
				icon = "▾ "
			}
			name := entry.node.name + "/"
			if isSelected {
				line = indent + styleAccentBlue.Render(icon) + styleAccentBlueBold.Render(name)
			} else {
				if entry.node.expanded {
					line = indent + styleAccentBlue.Render(icon) + ftDimDirName.Render(name)
				} else {
					line = indent + styleAccentBlue.Render(icon) + ftDirNameStyle.Render(name)
				}
			}
		} else {
			indent := strings.Repeat("  ", entry.depth)
			// Status icon
			var icon string
			switch entry.node.status {
			case state.StatusReviewed:
				icon = ftIconReviewedSt.Render("✓ ")
			case state.StatusModified:
				icon = ftIconModifiedSt.Render("⟳ ")
			default:
				icon = ftIconUnreviewSt.Render("○ ")
			}

			stats := ftAddClr.Render(fmt.Sprintf("+%d", entry.node.additions)) +
				" " + ftDelClr.Render(fmt.Sprintf("−%d", entry.node.deletions))

			// Skip reason tag for files excluded from AI review
			skipTag := ""
			if entry.node.skipReason != "" {
				skipTag = " " + styleTextMuted.Render("["+entry.node.skipReason+"]")
			}

			name := entry.node.name
			if isSelected {
				line = indent + icon + styleAccentBlueBold.Render(name) + "  " + stats + skipTag
			} else {
				line = indent + icon + styleTextSecondary.Render(name) + "  " + stats + skipTag
			}
		}

		// Highlight selected row with a left border indicator
		if isSelected {
			line = ftSelectedMarkerSt.Render("▌") + line
		} else {
			line = " " + line
		}

		// Truncate to panel width to prevent wrapping (Golden Rule 2)
		if ft.width > 0 && ansi.StringWidth(line) > ft.width {
			line = truncateToWidth(line, ft.width)
		}

		lines = append(lines, line)
	}

	// Pad remaining lines
	for len(lines) < ft.height {
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// selectByPath moves the cursor to the file entry matching path.
// If the file is inside a collapsed directory, parent dirs are expanded
// and the tree is re-flattened. Returns true if the path was found.
func (ft *fileTree) selectByPath(path string) bool {
	// First try finding it in the current flat list.
	for i, entry := range ft.flat {
		if entry.node.path == path {
			ft.cursor = i
			ft.ensureVisible()
			return true
		}
	}

	// Not visible — expand all directories and re-flatten, then search.
	expandAll(ft.root)
	ft.flatten()

	for i, entry := range ft.flat {
		if entry.node.path == path {
			ft.cursor = i
			ft.ensureVisible()
			return true
		}
	}

	return false
}

// expandAll recursively expands all directory nodes.
func expandAll(node *treeNode) {
	if node.isDir {
		node.expanded = true
		for _, child := range node.children {
			expandAll(child)
		}
	}
}

// maxContentWidth returns the widest line needed to display all entries
// without truncation. Counts: marker(1) + indent + icon(2) + name + gap(2) + stats.
func (ft *fileTree) maxContentWidth() int {
	maxW := 0
	for _, entry := range ft.flat {
		w := 1 // left marker/space
		if entry.node.isOverview {
			w += 2 + len(entry.node.name) // "◆ " + name
		} else if entry.node.isDir {
			w += entry.depth*2 + 2 + len(entry.node.name) + 1 // indent + icon + name + "/"
		} else {
			// indent + icon(2) + name + "  " + "+N -N"
			stats := fmt.Sprintf("+%d -%d", entry.node.additions, entry.node.deletions)
			w += entry.depth*2 + 2 + len(entry.node.name) + 2 + len(stats)
		}
		if w > maxW {
			maxW = w
		}
	}
	return maxW
}
