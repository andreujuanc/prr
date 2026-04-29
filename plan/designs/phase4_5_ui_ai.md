# Phase 4 & 5: UI Skeleton & AI Integration Design

## Objective
Establish the 3-column IDE-style layout, enabling contextual AI chats for both individual files and the entire PR.

## The 3-Column Layout
- **Left (20%)**: File List (`bubbles/list`).
  - Includes a special top-level item: `[PR Overview]`.
- **Middle (55%)**: Content View (`bubbles/viewport`).
  - If a file is selected: Shows the `delta` diff.
  - If `[PR Overview]` is selected: Shows the PR description.
- **Right (25%)**: AI Chat Panel.
  - Top: `bubbles/viewport` showing the chat history for the currently selected item.
  - Bottom: `bubbles/textarea` for user input.

## Contextual Contextual AI Routing
When the user types a message and hits Enter:
1. **If `[PR Overview]` is selected**: 
   - Context sent to LLM: Diffs of *all* files (truncated if exceeding limits).
   - Response appended to `global_chat`.
2. **If a file is selected**:
   - Context sent to LLM: Diff of *only that specific file*.
   - Response appended to `files["filepath"].chat`.

## Asynchronous Handling
- Sending an AI message spawns a background Goroutine that calls the LLM.
- The UI shows a loading indicator in the right pane.
- The Goroutine returns an `AIChatDeltaMsg` (for streaming) or `AIChatFinishedMsg`.