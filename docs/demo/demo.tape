# VHS tape file for prr demo
# Run with: vhs demo.tape

Output demo.gif

Set Theme "Dracula"
Set FontSize 14
Set Width 1200
Set Height 600
Set PlaybackSpeed 2
Set Padding 20

# Launch prr against PR #2
Type "./prr 2"
Sleep 500ms
Enter
Sleep 6s

# ── Review file 1: config.go ────────────────────────────────────────
# Down twice: PR Overview -> internal/config/ (dir) -> config.go
Down 2
Sleep 1s
Enter
Sleep 2s
Down 10
Sleep 2s
Space
Sleep 1s

# ── Review file 2: models_test.go ───────────────────────────────────
Type "n"
Sleep 2s
Down 15
Sleep 2s
Space
Sleep 1s

# ── Review file 3: .goreleaser.yml ──────────────────────────────────
Type "n"
Sleep 2s
Down 3
Sleep 2s
Space
Sleep 1s

# ── Full PR AI review ───────────────────────────────────────────────
# Switch focus back to file tree
Shift+Tab
Sleep 500ms

# Navigate up to PR Overview
Up
Up
Up
Up
Sleep 1s

# Run full AI review
Type "A"
Sleep 60s

# Let findings render
Sleep 3s

# Done
Ctrl+C
Sleep 1s
