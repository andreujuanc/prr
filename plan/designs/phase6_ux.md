# Phase 6: Interactivity & UX Design

## Interactivity & Keybindings

### Global Context
- `q` / `Ctrl+c`: Quit.
- `Tab` / `Shift+Tab`: Cycle focus between Left (List), Middle (Diff), and Right (Chat Input).

### Left Column (File List)
- `j`/`k` or `Up`/`Down`: Navigate.
  - **Debouncing required**: Wait ~150ms after the cursor stops moving before triggering the `git diff | delta` shell command to avoid CPU spikes.
- `Space`: Toggle review status of the highlighted file.
- `n` / `p`: Jump to next/prev unreviewed file.

### Middle Column (Diff Viewport)
- `j`/`k` / Page Up/Down: Scroll the diff.

### Right Column (Chat Input)
- `Enter`: Submit the chat prompt to the AI.

## UX Polish
- Highlight the active pane with a colored border (e.g., Cyan).
- Progress bar in the header updates instantly when `Space` is pressed.