package ui

import (
	"testing"

	"github.com/andreujuanc/prr/internal/state"
)

func TestFolderStatus_AllReviewed(t *testing.T) {
	dir := &treeNode{
		isDir: true,
		name:  "pkg",
		children: []*treeNode{
			{name: "a.go", status: state.StatusReviewed},
			{name: "b.go", status: state.StatusReviewed},
		},
	}
	if got := folderStatus(dir); got != state.StatusReviewed {
		t.Errorf("folderStatus = %q, want %q", got, state.StatusReviewed)
	}
}

func TestFolderStatus_SomeUnreviewed(t *testing.T) {
	dir := &treeNode{
		isDir: true,
		name:  "pkg",
		children: []*treeNode{
			{name: "a.go", status: state.StatusReviewed},
			{name: "b.go", status: state.StatusUnreviewed},
		},
	}
	if got := folderStatus(dir); got != state.StatusUnreviewed {
		t.Errorf("folderStatus = %q, want %q", got, state.StatusUnreviewed)
	}
}

func TestFolderStatus_HasModified(t *testing.T) {
	dir := &treeNode{
		isDir: true,
		name:  "pkg",
		children: []*treeNode{
			{name: "a.go", status: state.StatusReviewed},
			{name: "b.go", status: state.StatusModified},
		},
	}
	if got := folderStatus(dir); got != state.StatusModified {
		t.Errorf("folderStatus = %q, want %q", got, state.StatusModified)
	}
}

func TestFolderStatus_ModifiedAndUnreviewed(t *testing.T) {
	// Modified takes priority over unreviewed
	dir := &treeNode{
		isDir: true,
		name:  "pkg",
		children: []*treeNode{
			{name: "a.go", status: state.StatusUnreviewed},
			{name: "b.go", status: state.StatusModified},
		},
	}
	if got := folderStatus(dir); got != state.StatusModified {
		t.Errorf("folderStatus = %q, want %q", got, state.StatusModified)
	}
}

func TestFolderStatus_NestedDirs_AllReviewed(t *testing.T) {
	dir := &treeNode{
		isDir: true,
		name:  "src",
		children: []*treeNode{
			{
				isDir: true,
				name:  "pkg",
				children: []*treeNode{
					{name: "a.go", status: state.StatusReviewed},
				},
			},
			{
				isDir: true,
				name:  "cmd",
				children: []*treeNode{
					{name: "main.go", status: state.StatusReviewed},
				},
			},
		},
	}
	if got := folderStatus(dir); got != state.StatusReviewed {
		t.Errorf("folderStatus = %q, want %q", got, state.StatusReviewed)
	}
}

func TestFolderStatus_NestedDirs_OneUnreviewed(t *testing.T) {
	dir := &treeNode{
		isDir: true,
		name:  "src",
		children: []*treeNode{
			{
				isDir: true,
				name:  "pkg",
				children: []*treeNode{
					{name: "a.go", status: state.StatusReviewed},
				},
			},
			{
				isDir: true,
				name:  "cmd",
				children: []*treeNode{
					{name: "main.go", status: state.StatusUnreviewed},
				},
			},
		},
	}
	if got := folderStatus(dir); got != state.StatusUnreviewed {
		t.Errorf("folderStatus = %q, want %q", got, state.StatusUnreviewed)
	}
}

func TestFolderStatus_NestedDirs_OneModified(t *testing.T) {
	dir := &treeNode{
		isDir: true,
		name:  "src",
		children: []*treeNode{
			{
				isDir: true,
				name:  "pkg",
				children: []*treeNode{
					{name: "a.go", status: state.StatusReviewed},
				},
			},
			{
				isDir: true,
				name:  "cmd",
				children: []*treeNode{
					{name: "main.go", status: state.StatusModified},
				},
			},
		},
	}
	if got := folderStatus(dir); got != state.StatusModified {
		t.Errorf("folderStatus = %q, want %q", got, state.StatusModified)
	}
}

func TestFolderStatus_EmptyDir(t *testing.T) {
	dir := &treeNode{
		isDir:    true,
		name:     "empty",
		children: []*treeNode{},
	}
	if got := folderStatus(dir); got != state.StatusUnreviewed {
		t.Errorf("folderStatus(empty) = %q, want %q", got, state.StatusUnreviewed)
	}
}

func TestFolderStatus_FileNode(t *testing.T) {
	// Calling folderStatus on a file node just returns its own status
	file := &treeNode{name: "a.go", status: state.StatusReviewed}
	if got := folderStatus(file); got != state.StatusReviewed {
		t.Errorf("folderStatus(file) = %q, want %q", got, state.StatusReviewed)
	}
}

func TestFolderStatus_DeeplyNested(t *testing.T) {
	// 3 levels deep, all reviewed
	dir := &treeNode{
		isDir: true,
		name:  "a",
		children: []*treeNode{
			{
				isDir: true,
				name:  "b",
				children: []*treeNode{
					{
						isDir: true,
						name:  "c",
						children: []*treeNode{
							{name: "deep.go", status: state.StatusReviewed},
						},
					},
				},
			},
		},
	}
	if got := folderStatus(dir); got != state.StatusReviewed {
		t.Errorf("folderStatus(deep) = %q, want %q", got, state.StatusReviewed)
	}

	// Now mark the deep file as unreviewed
	dir.children[0].children[0].children[0].status = state.StatusUnreviewed
	if got := folderStatus(dir); got != state.StatusUnreviewed {
		t.Errorf("folderStatus(deep unreviewed) = %q, want %q", got, state.StatusUnreviewed)
	}
}

func TestFolderStatus_MixedDirAndFiles(t *testing.T) {
	// Dir with both direct files and subdirectories
	dir := &treeNode{
		isDir: true,
		name:  "root",
		children: []*treeNode{
			{name: "root.go", status: state.StatusReviewed},
			{
				isDir: true,
				name:  "sub",
				children: []*treeNode{
					{name: "sub.go", status: state.StatusReviewed},
				},
			},
		},
	}
	if got := folderStatus(dir); got != state.StatusReviewed {
		t.Errorf("folderStatus = %q, want %q", got, state.StatusReviewed)
	}

	// Make only the subdirectory file unreviewed
	dir.children[1].children[0].status = state.StatusUnreviewed
	if got := folderStatus(dir); got != state.StatusUnreviewed {
		t.Errorf("folderStatus = %q, want %q after nested change", got, state.StatusUnreviewed)
	}
}
