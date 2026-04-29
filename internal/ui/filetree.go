package ui

import (
	"fmt"
	"sort"
	"strings"

	"prr/internal/state"

	"github.com/charmbracelet/lipgloss"
)

// ── File tree types ─────────────────────────────────────────────────────

// treeNode represents a directory or file in the tree.
type treeNode struct {
	name      string         // just the segment name (e.g., "model.go")
	path      string         // full path (e.g., "internal/ui/model.go"), empty for dirs
	isDir     bool
	children  []*treeNode
	additions int
	deletions int
	status    state.ReviewStatus
	expanded  bool
}

// fileTree is a navigable file tree component.
type fileTree struct {
	root     *treeNode
	flat     []flatEntry // flattened visible entries for navigation
	cursor   int
	width    int
	height   int
	offset   int // scroll offset
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
					name:      part,
					path:      f.path,
					isDir:     false,
					additions: f.additions,
					deletions: f.deletions,
					status:    f.status,
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
type fileInfo struct {
	path      string
	additions int
	deletions int
	status    state.ReviewStatus
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
	ft.flattenNode(ft.root, -1) // root is invisible, children start at depth 0
}

func (ft *fileTree) flattenNode(node *treeNode, depth int) {
	if depth >= 0 {
		ft.flat = append(ft.flat, flatEntry{node: node, depth: depth})
	}
	if node.isDir && node.expanded {
		for _, child := range node.children {
			ft.flattenNode(child, depth+1)
		}
	}
}

// ── Navigation ──────────────────────────────────────────────────────────

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
		return ft.flat[ft.cursor].node.isDir
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
		return lipgloss.NewStyle().Foreground(textMuted).Render("  No files")
	}

	dirIcon := lipgloss.NewStyle().Foreground(accentBlue)
	dirCollapsed := lipgloss.NewStyle().Foreground(accentBlue)
	addClr := lipgloss.NewStyle().Foreground(accentGreen)
	delClr := lipgloss.NewStyle().Foreground(accentRed)
	selectedStyle := lipgloss.NewStyle().Foreground(accentBlue).Bold(true)
	normalStyle := lipgloss.NewStyle().Foreground(textSecondary)
	dirNameStyle := lipgloss.NewStyle().Foreground(accentBlue).Bold(true)
	dimDirName := lipgloss.NewStyle().Foreground(textMuted).Bold(true)

	var lines []string
	end := ft.offset + ft.height
	if end > len(ft.flat) {
		end = len(ft.flat)
	}

	for i := ft.offset; i < end; i++ {
		entry := ft.flat[i]
		indent := strings.Repeat("  ", entry.depth)
		isSelected := i == ft.cursor

		var line string
		if entry.node.isDir {
			icon := "▸ "
			if entry.node.expanded {
				icon = "▾ "
			}
			name := entry.node.name + "/"
			if isSelected {
				line = indent + dirIcon.Render(icon) + selectedStyle.Render(name)
			} else {
				if entry.node.expanded {
					line = indent + dirIcon.Render(icon) + dimDirName.Render(name)
				} else {
					line = indent + dirCollapsed.Render(icon) + dirNameStyle.Render(name)
				}
			}
		} else {
			// Status icon
			var icon string
			switch entry.node.status {
			case state.StatusReviewed:
				icon = lipgloss.NewStyle().Foreground(accentGreen).Render("✓ ")
			case state.StatusModified:
				icon = lipgloss.NewStyle().Foreground(accentYellow).Render("⟳ ")
			default:
				icon = lipgloss.NewStyle().Foreground(accentYellow).Render("○ ")
			}

			stats := addClr.Render(fmt.Sprintf("+%d", entry.node.additions)) +
				" " + delClr.Render(fmt.Sprintf("−%d", entry.node.deletions))

			name := entry.node.name
			if isSelected {
				line = indent + icon + selectedStyle.Render(name) + "  " + stats
			} else {
				line = indent + icon + normalStyle.Render(name) + "  " + stats
			}
		}

		// Highlight selected row with a left border indicator
		if isSelected {
			marker := lipgloss.NewStyle().Foreground(accentBlue).Bold(true).Render("▌")
			line = marker + line
		} else {
			line = " " + line
		}

		lines = append(lines, line)
	}

	// Pad remaining lines
	for len(lines) < ft.height {
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}
