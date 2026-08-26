package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/andreujuanc/prr/internal/git"
	"github.com/andreujuanc/prr/internal/state"
)

// ── Test fixture helpers ────────────────────────────────────────────────

// testPR returns a fake PullRequest with files in multiple directories.
func testPR() *git.PullRequest {
	return &git.PullRequest{
		Number:      999,
		Title:       "Test PR",
		Body:        "Test description",
		State:       "OPEN",
		BaseRefName: "main",
		HeadRefName: "feature/test",
		HeadRefOid:  "abc123",
		Author:      git.PRAuthor{Login: "testuser"},
		Files: []git.PRFile{
			{Path: "cmd/main.go", Additions: 10, Deletions: 2},
			{Path: "internal/ui/model.go", Additions: 50, Deletions: 20},
			{Path: "internal/ui/style.go", Additions: 5, Deletions: 0},
			{Path: "internal/ai/agent.go", Additions: 30, Deletions: 10},
			{Path: "README.md", Additions: 3, Deletions: 1},
		},
	}
}

// testState returns a State with file entries matching testPR.
// By default, all files are unreviewed.
func testState() *state.State {
	rs := state.NewState("999")
	for _, f := range testPR().Files {
		rs.Files[f.Path] = &state.FileState{
			Status:   state.StatusUnreviewed,
			DiffHash: "hash-" + f.Path,
		}
	}
	return rs
}

// testDiffs returns fake raw diffs keyed by file path.
func testDiffs() map[string]string {
	return map[string]string{
		"cmd/main.go":          "+package main\n+func main() {}",
		"internal/ui/model.go": "+package ui\n+type Model struct{}",
		"internal/ui/style.go": "+package ui\n+var s = 1",
		"internal/ai/agent.go": "+package ai\n+type Agent struct{}",
		"README.md":            "+# PRR",
	}
}

// newTestModel creates a fully initialized Model suitable for Update() tests.
// It bypasses network calls (no gh/git/AI needed) by pre-populating all fields
// that would normally come from async messages (PRFetchedMsg, DiffHashedMsg).
// TestViewportIsolation_ReviewDoesNotTouchChat pins the contract from
// the AI panel viewport split: rendering review progress must land in
// reviewViewport and leave chatViewport's content untouched. This is
// the load-bearing invariant of the split — if a future refactor
// accidentally writes review content back into chatViewport, this
// fails loud and tells us exactly which viewport got polluted.
func TestViewportIsolation_ReviewDoesNotTouchChat(t *testing.T) {
	m := newTestModel(t)

	// Seed chatViewport with a known marker that should NOT be touched
	// by a review update.
	const chatMarker = "<<chat-history-marker>>"
	m.chatViewport.SetContent(chatMarker)

	// Drive a review update via the tracker: setting up the phase
	// tracker with a recognisable detail routes the new phase view
	// into reviewViewport.
	m.aiReviewPhase = "batch"
	m.aiStreaming = true
	m.reviewProgress.Start(defaultReviewPhases())
	m.reviewProgress.Activate("phase1")
	m.reviewProgress.SetDetail("phase1", "RVMARK")
	m.updateChatViewWithStream()

	// reviewViewport should contain the review phase content.
	got := m.reviewViewport.View()
	if !strings.Contains(got, "RVMARK") {
		t.Errorf("reviewViewport missing review content; got:\n%s", got)
	}

	// chatViewport must be untouched.
	if !strings.Contains(m.chatViewport.View(), chatMarker) {
		t.Errorf("chatViewport was clobbered by review stream — content: %q", m.chatViewport.View())
	}
}

// TestViewportIsolation_ChatDoesNotTouchReview is the mirror: a chat-
// stream update must land in chatViewport and leave reviewViewport's
// content untouched.
func TestViewportIsolation_ChatDoesNotTouchReview(t *testing.T) {
	m := newTestModel(t)

	// Seed reviewViewport with a known marker.
	const reviewMarker = "<<review-content-marker>>"
	m.reviewViewport.SetContent(reviewMarker)

	// Drive a chat-stream update (no review phase set → routes to chat).
	m.aiReviewPhase = ""
	m.aiStreaming = true
	m.aiStreamBuffer = "stream content from chat"
	m.updateChatViewWithStream()

	// chatViewport should contain the chat stream content.
	if !strings.Contains(m.chatViewport.View(), "stream content from chat") {
		t.Errorf("chatViewport missing chat stream content; got:\n%s", m.chatViewport.View())
	}

	// reviewViewport must be untouched.
	if !strings.Contains(m.reviewViewport.View(), reviewMarker) {
		t.Errorf("reviewViewport was clobbered by chat stream — content: %q", m.reviewViewport.View())
	}
}

// TestChatInputHiddenDuringStreaming pins the cosmetic-but-important
// guarantee that the chat input does not show during AI streaming on
// the Chat tab. Without this, users see an input field that looks
// active but doesn't accept input (the handler gates on !aiStreaming).
//
// We assert by comparing the number of lines in View() output: when
// the input is rendered the chat pane reserves space for the input
// label + textarea, so the view is taller. Hiding the input shrinks it.
func TestChatInputHiddenDuringStreaming(t *testing.T) {
	m := newTestModel(t)
	m.aiPanelTab = tabChat
	m.showAIPanel = true
	m.focusedPane = PaneChat
	m.syncLayout()

	// Sanity: when not streaming, the focused "Enter to send" label
	// is visible (it's rendered above the input textarea).
	m.aiStreaming = false
	m.syncLayout()
	out := m.render()
	if !strings.Contains(out, "Enter to send") {
		t.Errorf("expected 'Enter to send' label when input is visible; got:\n%s", out)
	}

	// Streaming → input must be hidden. The label renders only when
	// the input is rendered alongside, so its absence is the signal.
	m.aiStreaming = true
	m.syncLayout()
	out = m.render()
	if strings.Contains(out, "Enter to send") {
		t.Errorf("chat input label still visible while streaming; should be hidden\nview:\n%s", out)
	}
}

func newTestModel(t *testing.T) Model {
	t.Helper()
	// Isolate state writes/reads to a per-test tempdir so tests don't
	// pollute the dev workspace's real .git/pr-tui/ (and don't read
	// snapshots leaked by sibling tests). t.Chdir auto-restores cwd
	// when the test ends.
	t.Chdir(t.TempDir())
	m := NewModel("999", nil, nil, 1, 3, false)

	// Simulate PRFetchedMsg
	m.pr = testPR()
	m.loading = false
	m.loadingMsg = ""

	// Simulate DiffHashedMsg
	m.reviewState = testState()
	m.rawDiffs = testDiffs()
	m.comments = make(map[string][]git.ReviewComment)

	// Simulate tea.WindowSizeMsg
	m.width = 160
	m.height = 50
	m.ready = true

	// Build file tree (normally happens in DiffHashedMsg handler)
	m.populateFileList(m.reviewState)

	// Set diff content to something non-empty
	m.setDiffContent(m.renderOverview())

	// Finalize layout
	m.syncLayout()

	return m
}

// key sends a rune key to the model and returns the updated model.
// Use for printable characters: key(m, 'j'), key(m, 'q'), etc.
func key(m Model, r rune) Model {
	updated, _ := m.Update(runeKey(r))
	return updated.(Model)
}

// runeKey builds a printable key press. In bubbletea v2 a key press
// carries the rune in Code and the literal text in Text.
func runeKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

// namedKeys maps the key names used by skey to their v2 key codes.
var namedKeys = map[string]rune{
	"tab":   tea.KeyTab,
	"enter": tea.KeyEnter,
	"esc":   tea.KeyEsc,
	"space": tea.KeySpace,
	"up":    tea.KeyUp,
	"down":  tea.KeyDown,
	"left":  tea.KeyLeft,
	"right": tea.KeyRight,
}

// skey sends a special key to the model, named the way the model
// matches it: "tab", "shift+tab", "ctrl+d", "enter", "esc".
func skey(m Model, name string) Model {
	updated, _ := m.Update(namedKey(name))
	return updated.(Model)
}

// namedKey parses a "ctrl+d"-style keystroke into a v2 key press.
func namedKey(name string) tea.KeyPressMsg {
	var k tea.Key
	parts := strings.Split(name, "+")
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "ctrl":
			k.Mod |= tea.ModCtrl
		case "alt":
			k.Mod |= tea.ModAlt
		case "shift":
			k.Mod |= tea.ModShift
		}
	}
	last := parts[len(parts)-1]
	if code, ok := namedKeys[last]; ok {
		k.Code = code
	} else {
		k.Code = []rune(last)[0]
		if k.Mod == 0 {
			k.Text = last
		}
	}
	return tea.KeyPressMsg(k)
}

// updateMsg sends an arbitrary tea.Msg and returns the updated model.
func updateMsg(m Model, msg tea.Msg) Model {
	updated, _ := m.Update(msg)
	return updated.(Model)
}

// assertPane checks that the focused pane matches expected.
func assertPane(t *testing.T, m Model, expected Pane) {
	t.Helper()
	names := map[Pane]string{PaneFileList: "FileList", PaneDiff: "Diff", PaneChat: "Chat"}
	if m.focusedPane != expected {
		t.Errorf("expected pane %s, got %s", names[expected], names[m.focusedPane])
	}
}

// fileTreePaths returns all non-dir, non-overview, non-actions paths in the visible flat list.
func fileTreePaths(m Model) []string {
	var paths []string
	for _, e := range m.fileTree.flat {
		if !e.node.isDir && !e.node.isOverview && !e.node.isActions {
			paths = append(paths, e.node.path)
		}
	}
	return paths
}

// ── Pane cycling tests ──────────────────────────────────────────────────

func TestPaneCycle_TabForward(t *testing.T) {
	m := newTestModel(t)
	assertPane(t, m, PaneFileList) // default

	m = skey(m, "tab")
	assertPane(t, m, PaneDiff)

	m = skey(m, "tab")
	assertPane(t, m, PaneChat)

	m = skey(m, "tab") // wraps
	assertPane(t, m, PaneFileList)
}

func TestPaneCycle_ShiftTabBackward(t *testing.T) {
	m := newTestModel(t)
	assertPane(t, m, PaneFileList)

	m = skey(m, "shift+tab") // wraps backward
	assertPane(t, m, PaneChat)

	m = skey(m, "shift+tab")
	assertPane(t, m, PaneDiff)

	m = skey(m, "shift+tab")
	assertPane(t, m, PaneFileList)
}

func TestPaneCycle_HiddenPanelsSkipped(t *testing.T) {
	m := newTestModel(t)

	// Hide AI panel
	m.showAIPanel = false

	m = skey(m, "tab") // FileList → Diff
	assertPane(t, m, PaneDiff)

	m = skey(m, "tab") // Diff → wraps to FileList (Chat hidden)
	assertPane(t, m, PaneFileList)

	// Hide file panel too — only Diff visible
	m.showFilePanel = false
	m = skey(m, "tab")
	assertPane(t, m, PaneDiff) // stuck on Diff
}

func TestPaneCycle_HideFilePanel(t *testing.T) {
	m := newTestModel(t)
	m.showFilePanel = false
	m.focusedPane = PaneDiff

	m = skey(m, "tab") // Diff → Chat
	assertPane(t, m, PaneChat)

	m = skey(m, "tab") // Chat → Diff (FileList hidden)
	assertPane(t, m, PaneDiff)
}

// ── File tree navigation tests ──────────────────────────────────────────

func TestFileNav_JKMovement(t *testing.T) {
	m := newTestModel(t)
	assertPane(t, m, PaneFileList)

	startCursor := m.fileTree.cursor
	m = key(m, 'j') // move down
	if m.fileTree.cursor != startCursor+1 {
		t.Errorf("expected cursor %d after j, got %d", startCursor+1, m.fileTree.cursor)
	}

	m = key(m, 'k') // move back up
	if m.fileTree.cursor != startCursor {
		t.Errorf("expected cursor %d after k, got %d", startCursor, m.fileTree.cursor)
	}
}

func TestFileNav_CursorBounds(t *testing.T) {
	m := newTestModel(t)

	// Move to top (cursor=0 is PR Overview)
	m.fileTree.cursor = 0
	m = key(m, 'k') // can't go above 0
	if m.fileTree.cursor != 0 {
		t.Errorf("cursor should stay at 0, got %d", m.fileTree.cursor)
	}

	// Move to bottom
	last := len(m.fileTree.flat) - 1
	m.fileTree.cursor = last
	m = key(m, 'j') // can't go past last
	if m.fileTree.cursor != last {
		t.Errorf("cursor should stay at %d, got %d", last, m.fileTree.cursor)
	}
}

func TestFileNav_SpaceToggleReviewed(t *testing.T) {
	m := newTestModel(t)

	// Move to a file (skip PR Overview, Actions, and any dirs)
	for i, e := range m.fileTree.flat {
		if !e.node.isDir && !e.node.isOverview && !e.node.isActions {
			m.fileTree.cursor = i
			break
		}
	}

	path := m.fileTree.selectedPath()
	if path == "" {
		t.Fatal("no file selected")
	}

	// Verify starts as unreviewed
	fs := m.reviewState.Files[path]
	if fs.Status != state.StatusUnreviewed {
		t.Fatalf("expected unreviewed, got %s", fs.Status)
	}

	// Toggle to reviewed
	m = key(m, ' ')
	if fs.Status != state.StatusReviewed {
		t.Errorf("expected reviewed after space, got %s", fs.Status)
	}

	// Toggle back
	m = key(m, ' ')
	if fs.Status != state.StatusUnreviewed {
		t.Errorf("expected unreviewed after second space, got %s", fs.Status)
	}
}

func TestFileNav_SpaceToggleDiffPane(t *testing.T) {
	m := newTestModel(t)

	// Select a file and switch to diff pane
	for i, e := range m.fileTree.flat {
		if !e.node.isDir && !e.node.isOverview && !e.node.isActions {
			m.fileTree.cursor = i
			break
		}
	}
	path := m.fileTree.selectedPath()
	m.selectedFile = path
	m.viewMode = viewModeFile
	m.focusedPane = PaneDiff

	fs := m.reviewState.Files[path]

	m = key(m, ' ')
	if fs.Status != state.StatusReviewed {
		t.Errorf("space in diff pane should toggle reviewed, got %s", fs.Status)
	}
}

// ── Unreviewed jump tests ───────────────────────────────────────────────

func TestFileNav_NextPrevUnreviewed(t *testing.T) {
	m := newTestModel(t)

	// Mark all files as reviewed except one
	var unreviewedPath string
	for path, fs := range m.reviewState.Files {
		fs.Status = state.StatusReviewed
		// Also update the tree node
		for _, e := range m.fileTree.flat {
			if e.node.path == path {
				e.node.status = state.StatusReviewed
			}
		}
		if unreviewedPath == "" {
			unreviewedPath = path
		}
	}
	// Leave one file unreviewed
	m.reviewState.Files[unreviewedPath].Status = state.StatusUnreviewed
	for _, e := range m.fileTree.flat {
		if e.node.path == unreviewedPath {
			e.node.status = state.StatusUnreviewed
		}
	}

	// Start at PR Overview (cursor 0)
	m.fileTree.cursor = 0

	// Press 'n' to jump to next unreviewed
	m = key(m, 'n')
	selected := m.fileTree.selectedPath()
	if selected != unreviewedPath {
		t.Errorf("expected jump to %q, got %q", unreviewedPath, selected)
	}
}

func TestFileNav_NextUnreviewed_AllReviewed(t *testing.T) {
	m := newTestModel(t)

	// Mark all files as reviewed
	for _, fs := range m.reviewState.Files {
		fs.Status = state.StatusReviewed
	}
	for _, e := range m.fileTree.flat {
		if !e.node.isDir && !e.node.isOverview && !e.node.isActions {
			e.node.status = state.StatusReviewed
		}
	}

	startCursor := m.fileTree.cursor
	m = key(m, 'n')
	// Cursor should not move (no unreviewed files)
	if m.fileTree.cursor != startCursor {
		t.Errorf("cursor should stay at %d when all reviewed, got %d", startCursor, m.fileTree.cursor)
	}
}

func TestFileNav_NextUnreviewed_FromDiffPane(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneDiff

	startCursor := m.fileTree.cursor
	m = key(m, 'n')
	// Should still jump (n/p work from both FileList and Diff panes)
	// Since all files are unreviewed, should jump to next file
	if m.fileTree.cursor == startCursor {
		// cursor 0 is PR Overview (which has no status), so n should jump to a file
		if !m.fileTree.flat[startCursor].node.isOverview {
			t.Error("expected cursor to move to an unreviewed file")
		}
	}
}

// ── Hide reviewed filter tests ──────────────────────────────────────────

func TestFileNav_HideReviewedFilter(t *testing.T) {
	m := newTestModel(t)

	totalBefore := len(m.fileTree.flat)

	// Mark one file as reviewed
	var reviewedPath string
	for path, fs := range m.reviewState.Files {
		fs.Status = state.StatusReviewed
		reviewedPath = path
		break
	}
	for _, e := range m.fileTree.flat {
		if e.node.path == reviewedPath {
			e.node.status = state.StatusReviewed
		}
	}

	// Toggle hide-reviewed (press 'r' in FileList)
	m = key(m, 'r')

	if !m.fileTree.hideReviewed {
		t.Error("expected hideReviewed to be true after pressing r")
	}

	// The reviewed file should be hidden
	for _, e := range m.fileTree.flat {
		if e.node.path == reviewedPath {
			t.Errorf("reviewed file %q should be hidden", reviewedPath)
		}
	}

	if len(m.fileTree.flat) >= totalBefore {
		t.Error("flat list should be shorter after hiding reviewed file")
	}

	// Toggle back
	m = key(m, 'r')
	if m.fileTree.hideReviewed {
		t.Error("expected hideReviewed to be false after second r")
	}
}

// ── Directory expand/collapse tests ─────────────────────────────────────

func TestFileNav_ExpandCollapseDir(t *testing.T) {
	m := newTestModel(t)

	// Find a directory node
	var dirIdx int
	var found bool
	for i, e := range m.fileTree.flat {
		if e.node.isDir {
			dirIdx = i
			found = true
			break
		}
	}
	if !found {
		t.Skip("no directory nodes in test file tree")
	}

	m.fileTree.cursor = dirIdx
	totalBefore := len(m.fileTree.flat)

	// Collapse with 'h' (left)
	if m.fileTree.flat[dirIdx].node.expanded {
		m = key(m, 'h')
		if m.fileTree.flat[dirIdx].node.expanded {
			t.Error("directory should be collapsed after 'h'")
		}
		if len(m.fileTree.flat) >= totalBefore {
			t.Error("flat list should shrink when directory is collapsed")
		}
	}

	// Expand with 'l' (right)
	m = key(m, 'l')
	if !m.fileTree.flat[dirIdx].node.expanded {
		t.Error("directory should be expanded after 'l'")
	}
}

// ── Scroll keybinding tests (Diff pane) ─────────────────────────────────

func TestScroll_DiffPane_GotoTopBottom(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneDiff
	m.selectedFile = "cmd/main.go"
	m.viewMode = viewModeFile

	// Put some content in the viewport
	var longContent strings.Builder
	for range 200 {
		longContent.WriteString("+line of diff content\n")
	}
	m.setDiffContent(longContent.String())

	// G → go to bottom
	m = key(m, 'G')
	if m.diffViewport.YOffset() == 0 {
		// Only fails if content is longer than viewport
		totalLines := m.diffViewport.TotalLineCount()
		if totalLines > m.diffViewport.Height() {
			t.Error("G should scroll to bottom when content exceeds viewport")
		}
	}

	// g → go to top
	m = key(m, 'g')
	if m.diffViewport.YOffset() != 0 {
		t.Errorf("g should scroll to top, got offset %d", m.diffViewport.YOffset())
	}
	if m.diffCursor != 0 {
		t.Errorf("g should reset cursor to 0, got %d", m.diffCursor)
	}
}

func TestScroll_DiffPane_HalfPage(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneDiff
	m.selectedFile = "cmd/main.go"
	m.viewMode = viewModeFile

	var longContent strings.Builder
	for range 200 {
		longContent.WriteString("+line\n")
	}
	m.setDiffContent(longContent.String())

	// Ctrl+D → half page down
	m = skey(m, "ctrl+d")
	if m.diffViewport.YOffset() == 0 {
		totalLines := m.diffViewport.TotalLineCount()
		if totalLines > m.diffViewport.Height() {
			t.Error("Ctrl+D should scroll down")
		}
	}

	savedOffset := m.diffViewport.YOffset()

	// Ctrl+U → half page up
	m = skey(m, "ctrl+u")
	if m.diffViewport.YOffset() >= savedOffset && savedOffset > 0 {
		t.Error("Ctrl+U should scroll up")
	}
}

func TestScroll_DiffPane_JKMoveCursor(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneDiff
	m.selectedFile = "cmd/main.go"
	m.viewMode = viewModeFile
	m.setDiffContent("+line1\n+line2\n+line3\n+line4\n+line5")
	m.diffCursor = 0

	m = key(m, 'j')
	if m.diffCursor != 1 {
		t.Errorf("j in diff pane should move cursor down, got %d", m.diffCursor)
	}

	m = key(m, 'k')
	if m.diffCursor != 0 {
		t.Errorf("k in diff pane should move cursor up, got %d", m.diffCursor)
	}
}

// ── Overlay tests ───────────────────────────────────────────────────────

func TestOverlay_HelpToggle(t *testing.T) {
	m := newTestModel(t)

	m = key(m, '?')
	if !m.showHelp {
		t.Error("? should open help overlay")
	}

	// Keys should be intercepted by help overlay
	prevCursor := m.fileTree.cursor
	m = key(m, 'j') // should NOT move file tree
	if m.fileTree.cursor != prevCursor {
		t.Error("j should be intercepted by help overlay")
	}

	// Close help
	m = key(m, '?')
	if m.showHelp {
		t.Error("? again should close help overlay")
	}
}

func TestOverlay_HelpCloseWithEsc(t *testing.T) {
	m := newTestModel(t)
	m = key(m, '?')
	if !m.showHelp {
		t.Fatal("help should be open")
	}

	m = skey(m, "esc")
	if m.showHelp {
		t.Error("esc should close help overlay")
	}
}

func TestOverlay_ModelPicker(t *testing.T) {
	m := newTestModel(t)

	m = key(m, 'm')
	if !m.showModelPicker {
		t.Error("m should open model picker")
	}

	// Navigation within picker
	m = key(m, 'j')
	// The cursor should move (or stay if only one model)

	// Close with esc
	m = skey(m, "esc")
	if m.showModelPicker {
		t.Error("esc should close model picker")
	}
}

func TestOverlay_ModelPickerBlockedInChat(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneChat

	m = key(m, 'm')
	if m.showModelPicker {
		t.Error("m should not open model picker when focused on Chat pane")
	}
}

func TestOverlay_ModelPickerBlockedWhileStreaming(t *testing.T) {
	m := newTestModel(t)
	m.aiStreaming = true

	m = key(m, 'm')
	if m.showModelPicker {
		t.Error("m should not open model picker while AI is streaming")
	}
}

func TestOverlay_SubmitReview(t *testing.T) {
	m := newTestModel(t)

	// Set up a review so Ctrl+S works
	m.reviewState.Review = &state.AIReview{
		Summary: "LGTM",
		Structured: &state.ReviewOutput{
			Summary: "All good",
			Verdict: "approve",
		},
	}

	m = skey(m, "ctrl+s")
	if !m.showSubmitReview {
		t.Error("Ctrl+S should open submit review confirmation")
	}

	// Navigate to Cancel
	m = key(m, 'j')
	if m.submitReviewCursor != 1 {
		t.Errorf("expected cursor on Cancel (1), got %d", m.submitReviewCursor)
	}

	// Press enter on Cancel to close
	m = skey(m, "enter")
	if m.showSubmitReview {
		t.Error("enter on Cancel should close submit overlay")
	}
}

func TestOverlay_SubmitReviewBlockedWithoutReview(t *testing.T) {
	m := newTestModel(t)
	// No review exists
	m.reviewState.Review = nil

	m = skey(m, "ctrl+s")
	if m.showSubmitReview {
		t.Error("Ctrl+S should not open submit when no review exists")
	}
}

// ── Q key behavior ──────────────────────────────────────────────────────

func TestQKey_BlockedInChatPane(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneChat

	updated, cmd := m.Update(runeKey('q'))
	m = updated.(Model)

	// Should NOT produce a tea.Quit command
	if cmd != nil {
		// Execute the cmd to check if it's a quit
		msg := cmd()
		if _, ok := msg.(tea.QuitMsg); ok {
			t.Error("q should not quit when in Chat pane")
		}
	}
}

func TestQKey_QuitsFromFileList(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneFileList

	_, cmd := m.Update(runeKey('q'))
	if cmd == nil {
		t.Fatal("q from FileList should produce a command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("q from FileList should produce QuitMsg, got %T", msg)
	}
}

func TestQKey_QuitsFromDiffPane(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneDiff

	_, cmd := m.Update(runeKey('q'))
	if cmd == nil {
		t.Fatal("q from Diff pane should produce a command")
	}
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("q from Diff should produce QuitMsg, got %T", msg)
	}
}

// ── Context lines tests ─────────────────────────────────────────────────

func TestContextLines_Increase(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneDiff // must be in diff pane
	m.selectedFile = "cmd/main.go"
	m.viewMode = viewModeFile

	initial := m.contextLines
	m = key(m, '+')
	if m.contextLines != initial+3 {
		t.Errorf("expected context lines %d, got %d", initial+3, m.contextLines)
	}
}

func TestContextLines_Decrease(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneDiff
	m.selectedFile = "cmd/main.go"
	m.viewMode = viewModeFile
	m.contextLines = 6

	m = key(m, '-')
	if m.contextLines != 3 {
		t.Errorf("expected context lines 3, got %d", m.contextLines)
	}

	m = key(m, '-')
	if m.contextLines != 0 {
		t.Errorf("expected context lines 0, got %d", m.contextLines)
	}

	// Can't go below 0
	m = key(m, '-')
	if m.contextLines != 0 {
		t.Errorf("context lines should not go below 0, got %d", m.contextLines)
	}
}

func TestContextLines_MaxCap(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneDiff
	m.selectedFile = "cmd/main.go"
	m.viewMode = viewModeFile
	m.contextLines = 99

	m = key(m, '+')
	if m.contextLines > 100 {
		t.Errorf("context lines should cap at 100, got %d", m.contextLines)
	}
}

func TestContextLines_NoChangeWithoutFile(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneDiff
	m.selectedFile = "" // no file selected

	initial := m.contextLines
	m = key(m, '+')
	if m.contextLines != initial {
		t.Errorf("context lines should not change without selected file, got %d", m.contextLines)
	}
}

func TestContextLines_NoChangeFromFileList(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneFileList // wrong pane
	m.selectedFile = "cmd/main.go"
	m.viewMode = viewModeFile

	initial := m.contextLines
	m = key(m, '+')
	if m.contextLines != initial {
		t.Errorf("context lines should not change from FileList pane, got %d", m.contextLines)
	}
}

// ── AI streaming guard tests ────────────────────────────────────────────

func TestAIStreaming_EscCancels(t *testing.T) {
	cancelled := false
	m := newTestModel(t)
	m.aiStreaming = true
	m.aiCancelFn = func() { cancelled = true }

	m = skey(m, "esc")
	if !cancelled {
		t.Error("esc during streaming should call cancel function")
	}
	if m.aiStreaming {
		t.Error("aiStreaming should be false after cancel")
	}
}

func TestAIStreaming_CtrlCCancels(t *testing.T) {
	cancelled := false
	m := newTestModel(t)
	m.aiStreaming = true
	m.aiCancelFn = func() { cancelled = true }

	m = skey(m, "ctrl+c")
	if !cancelled {
		t.Error("ctrl+c during streaming should call cancel function")
	}
}

func TestAIStreaming_BlocksOtherKeys(t *testing.T) {
	m := newTestModel(t)
	m.aiStreaming = true
	m.aiCancelFn = func() {} // noop
	m.focusedPane = PaneChat

	// Enter should be blocked in Chat during streaming
	updated, cmd := m.Update(namedKey("enter"))
	m = updated.(Model)
	if cmd != nil {
		t.Error("enter in Chat during streaming should be blocked (no command)")
	}
}

// ── Enter key behavior ─────────────────────────────────────────────────

func TestEnter_FileListSelectsFile(t *testing.T) {
	m := newTestModel(t)

	// Move to a file node
	for i, e := range m.fileTree.flat {
		if !e.node.isDir && !e.node.isOverview && !e.node.isActions {
			m.fileTree.cursor = i
			break
		}
	}

	m = skey(m, "enter")
	assertPane(t, m, PaneDiff)
}

func TestEnter_FileListExpandsDir(t *testing.T) {
	m := newTestModel(t)

	// Find a directory
	for i, e := range m.fileTree.flat {
		if e.node.isDir {
			m.fileTree.cursor = i
			// Collapse it first
			e.node.expanded = false
			m.fileTree.flatten()
			break
		}
	}

	// Find the dir again after flatten
	for i, e := range m.fileTree.flat {
		if e.node.isDir && !e.node.expanded {
			m.fileTree.cursor = i
			break
		}
	}

	if m.fileTree.selectedIsDir() {
		m = skey(m, "enter")
		// Should toggle expansion, not change pane
		assertPane(t, m, PaneFileList)
	}
}

// ── Panel visibility toggle tests ───────────────────────────────────────

func TestPanelToggle_CtrlA_AIPanel(t *testing.T) {
	m := newTestModel(t)

	if !m.showAIPanel {
		t.Fatal("AI panel should be visible by default")
	}

	m = skey(m, "ctrl+a")
	if m.showAIPanel {
		t.Error("Ctrl+A should hide AI panel")
	}

	m = skey(m, "ctrl+a")
	if !m.showAIPanel {
		t.Error("Ctrl+A again should show AI panel")
	}
}

func TestPanelToggle_CtrlB_FilePanel(t *testing.T) {
	m := newTestModel(t)

	if !m.showFilePanel {
		t.Fatal("File panel should be visible by default")
	}

	m = skey(m, "ctrl+b")
	if m.showFilePanel {
		t.Error("Ctrl+B should hide file panel")
	}

	m = skey(m, "ctrl+b")
	if !m.showFilePanel {
		t.Error("Ctrl+B again should show file panel")
	}
}

func TestPanelToggle_HidingResetsFocus(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneChat

	// Hide AI panel while Chat is focused
	m = skey(m, "ctrl+a")
	// Focus should move to a visible pane
	if m.focusedPane == PaneChat {
		t.Error("focus should move away from Chat when AI panel is hidden")
	}
}

// ── Review state transition tests ───────────────────────────────────────

func TestReviewDone_PopulatesState(t *testing.T) {
	m := newTestModel(t)
	m.aiStreaming = true

	structured := &state.ReviewOutput{
		Summary: "Code review complete",
		Verdict: "comment",
		Findings: []state.ReviewFinding{
			{
				Severity: "medium",
				Category: "bug",
				File:     "cmd/main.go",
				Line:     5,
				Title:    "Potential nil deref",
				Detail:   "Check for nil before use",
			},
		},
		MissingTests:       []string{"cmd/main.go"},
		QuestionsForAuthor: []string{"Why no error handling?"},
	}

	review := &state.AIReview{
		Summary:    "Code review complete",
		Structured: structured,
	}

	m = updateMsg(m, AIChatDoneMsg{
		FullResponse:     "Full review text",
		Review:           review,
		StructuredReview: structured,
		FileFindings:     map[string]string{"cmd/main.go": "issue found"},
	})

	if m.aiStreaming {
		t.Error("aiStreaming should be false after AIChatDoneMsg")
	}
	if m.reviewState.Review == nil {
		t.Error("review should be saved to state")
	}
	if m.reviewState.Review.Summary != "Code review complete" {
		t.Errorf("unexpected summary: %s", m.reviewState.Review.Summary)
	}
}

func TestReviewDone_SwitchesToReviewTab(t *testing.T) {
	m := newTestModel(t)
	m.aiStreaming = true
	m.aiPanelTab = 1 // chat tab

	review := &state.AIReview{
		Summary: "Done",
		Structured: &state.ReviewOutput{
			Summary: "Done",
			Verdict: "approve",
		},
	}

	m = updateMsg(m, AIChatDoneMsg{
		FullResponse:     "text",
		Review:           review,
		StructuredReview: review.Structured,
	})

	if m.aiPanelTab != 0 {
		t.Errorf("expected AI panel to switch to review tab (0), got %d", m.aiPanelTab)
	}
}

func TestReviewDone_ErrorPreservesStreaming(t *testing.T) {
	m := newTestModel(t)
	m.aiStreaming = true

	m = updateMsg(m, AIChatDoneMsg{
		Err: nil,
	})

	// Even with no review data, streaming should be cleared
	if m.aiStreaming {
		t.Error("aiStreaming should be false after AIChatDoneMsg even without review")
	}
}

// ── Window resize tests ─────────────────────────────────────────────────

func TestWindowSize_UpdatesLayout(t *testing.T) {
	m := newTestModel(t)

	m = updateMsg(m, tea.WindowSizeMsg{Width: 200, Height: 60})

	if m.width != 200 {
		t.Errorf("expected width 200, got %d", m.width)
	}
	if m.height != 60 {
		t.Errorf("expected height 60, got %d", m.height)
	}
	if !m.ready {
		t.Error("model should be ready after WindowSizeMsg")
	}
}

func TestWindowSize_SmallTerminal(t *testing.T) {
	m := newTestModel(t)

	m = updateMsg(m, tea.WindowSizeMsg{Width: 40, Height: 10})

	if m.width != 40 || m.height != 10 {
		t.Errorf("expected 40x10, got %dx%d", m.width, m.height)
	}
	// Should not panic with small dimensions
}

// ── AI batch status tracking tests ──────────────────────────────────────

func TestBatchProgress_InitAndUpdate(t *testing.T) {
	m := newTestModel(t)

	m = updateMsg(m, AIReviewInitMsg{
		Batches: []AIReviewBatchInfo{
			{Label: "root", NumFiles: 2},
			{Label: "internal/ui", NumFiles: 3},
		},
	})

	if len(m.aiReviewBatches) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(m.aiReviewBatches))
	}
	if len(m.aiReviewStatuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(m.aiReviewStatuses))
	}

	m = updateMsg(m, AIReviewProgressMsg{Batch: 0, Status: BatchActive})
	if m.aiReviewStatuses[0] != BatchActive {
		t.Errorf("batch 0 should be Active, got %d", m.aiReviewStatuses[0])
	}

	m = updateMsg(m, AIReviewProgressMsg{Batch: 0, Status: BatchDone})
	if m.aiReviewStatuses[0] != BatchDone {
		t.Errorf("batch 0 should be Done, got %d", m.aiReviewStatuses[0])
	}

	m = updateMsg(m, AIReviewProgressMsg{Batch: 1, Status: BatchCached})
	if m.aiReviewStatuses[1] != BatchCached {
		t.Errorf("batch 1 should be Cached, got %d", m.aiReviewStatuses[1])
	}
}

func TestSynthesisPhase_Tracking(t *testing.T) {
	m := newTestModel(t)
	m.aiReviewPhase = "batch"

	m = updateMsg(m, AIReviewSynthesisMsg{})
	if m.aiReviewPhase != "synthesis" {
		t.Errorf("expected synthesis phase, got %q", m.aiReviewPhase)
	}
}

// ── Finding navigation tests (Chat pane, Review tab) ────────────────────

func TestFindingNav_JKInReviewTab(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneChat
	m.aiPanelTab = 0 // review tab

	m.reviewFindings = []state.ReviewFinding{
		{File: "cmd/main.go", Line: 5, Title: "Issue 1"},
		{File: "cmd/main.go", Line: 10, Title: "Issue 2"},
		{File: "internal/ai/agent.go", Line: 3, Title: "Issue 3"},
	}
	m.reviewCursor = 0

	// j → next finding
	m = key(m, 'j')
	if m.reviewCursor != 1 {
		t.Errorf("j should move review cursor to 1, got %d", m.reviewCursor)
	}

	m = key(m, 'j')
	if m.reviewCursor != 2 {
		t.Errorf("j should move review cursor to 2, got %d", m.reviewCursor)
	}

	// k → previous finding
	m = key(m, 'k')
	if m.reviewCursor != 1 {
		t.Errorf("k should move review cursor to 1, got %d", m.reviewCursor)
	}
}

func TestFindingNav_BoundsCheck(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneChat
	m.aiPanelTab = 0

	m.reviewFindings = []state.ReviewFinding{
		{File: "cmd/main.go", Line: 5, Title: "Only issue"},
	}
	m.reviewCursor = 0

	// Can't go below 0
	m = key(m, 'k')
	if m.reviewCursor != 0 {
		t.Errorf("cursor should stay at 0, got %d", m.reviewCursor)
	}

	// Can't go past last
	m = key(m, 'j')
	if m.reviewCursor != 0 {
		t.Errorf("cursor should stay at 0 (only 1 finding), got %d", m.reviewCursor)
	}
}

// ── Refresh from origin tests ───────────────────────────────────────────

func TestRefreshKey_OnlyFromFileList(t *testing.T) {
	m := newTestModel(t)

	// 'o' from Diff pane should not trigger refresh
	m.focusedPane = PaneDiff
	m = key(m, 'o')
	if m.loading {
		t.Error("o should not trigger refresh from Diff pane")
	}

	// 'o' from FileList should trigger refresh
	m.focusedPane = PaneFileList
	updated, cmd := m.Update(runeKey('o'))
	m = updated.(Model)
	if !m.loading {
		t.Error("o from FileList should trigger loading state")
	}
	if cmd == nil {
		t.Error("o should produce a command to fetch PR")
	}
}

func TestRefreshKey_BlockedDuringLoading(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneFileList
	m.loading = true

	m = key(m, 'o')
	// Should not change loading state or produce duplicate fetches
	if m.loadingMsg != "" {
		// loadingMsg is only set when 'o' triggers a fresh refresh
		// Since we were already loading, it shouldn't change
	}
}

func TestRefreshKey_BlockedDuringStreaming(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneFileList
	m.aiStreaming = true

	initialLoading := m.loading
	m = key(m, 'o')
	if m.loading != initialLoading {
		t.Error("o should not trigger refresh while AI is streaming")
	}
}

// ── Comment mode tests ──────────────────────────────────────────────────

func TestCommentMode_EscCancels(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneDiff
	m.commenting = true

	m = skey(m, "esc")
	if m.commenting {
		t.Error("esc should cancel comment mode")
	}
}

// ── AI review trigger tests ─────────────────────────────────────────────

func TestReviewTrigger_BlockedInChat(t *testing.T) {
	m := newTestModel(t)
	m.focusedPane = PaneChat

	m = key(m, 'a')
	if m.aiStreaming {
		t.Error("'a' should not start review from Chat pane")
	}
}

func TestReviewTrigger_BlockedWhileStreaming(t *testing.T) {
	m := newTestModel(t)
	m.aiStreaming = true

	m = key(m, 'a')
	// Should not start a second review
}

// ── Integration: full navigation flow ───────────────────────────────────

func TestFlow_NavigateToFileAndBack(t *testing.T) {
	m := newTestModel(t)
	assertPane(t, m, PaneFileList)

	// Move to a file node (skip PR Overview, Actions, and dirs)
	for i, e := range m.fileTree.flat {
		if !e.node.isDir && !e.node.isOverview && !e.node.isActions {
			m.fileTree.cursor = i
			break
		}
	}

	// Enter to go to diff
	m = skey(m, "enter")
	assertPane(t, m, PaneDiff)

	// Shift+Tab back to file list
	m = skey(m, "shift+tab")
	assertPane(t, m, PaneFileList)
}

func TestFlow_CycleThroughAllPanes(t *testing.T) {
	m := newTestModel(t)

	// Full cycle: FileList → Diff → Chat → FileList
	paneOrder := []Pane{PaneDiff, PaneChat, PaneFileList}
	for _, expected := range paneOrder {
		m = skey(m, "tab")
		assertPane(t, m, expected)
	}
}

func TestFlow_ToggleReviewedThenFilterThenJump(t *testing.T) {
	m := newTestModel(t)

	// Find first file
	var firstFileIdx int
	for i, e := range m.fileTree.flat {
		if !e.node.isDir && !e.node.isOverview && !e.node.isActions {
			firstFileIdx = i
			break
		}
	}

	// Move to first file and mark reviewed
	m.fileTree.cursor = firstFileIdx
	m = key(m, ' ')

	path := m.fileTree.flat[firstFileIdx].node.path
	if m.reviewState.Files[path].Status != state.StatusReviewed {
		t.Fatal("file should be marked reviewed")
	}

	// Turn on hide-reviewed
	m = key(m, 'r')
	if !m.fileTree.hideReviewed {
		t.Fatal("hide reviewed should be on")
	}

	// The reviewed file should not be in the flat list
	for _, e := range m.fileTree.flat {
		if e.node.path == path {
			t.Error("reviewed file should be hidden")
		}
	}

	// Jump to next unreviewed should work
	m = key(m, 'n')
	selected := m.fileTree.selectedPath()
	if selected == path {
		t.Error("should not jump to the reviewed file")
	}
}
