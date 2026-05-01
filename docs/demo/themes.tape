# VHS tape file for prr theme switcher demo
# Run from repo root: vhs docs/demo/themes.tape

Output docs/demo/themes.gif

Set FontSize 14
Set Width 1200
Set Height 600
Set PlaybackSpeed 1.5
Set Padding 20

# Launch prr against a PR
Type "./prr 6"
Sleep 500ms
Enter
Sleep 6s

# Select a file to show some diff content
Down 2
Sleep 500ms
Enter
Sleep 3s

# Open theme picker
Type "T"
Sleep 2s

# Cycle through themes — each j/k live-previews
Down
Sleep 2s

Down
Sleep 2s

Down
Sleep 2s

Down
Sleep 2s

Down
Sleep 2s

Down
Sleep 2s

Down
Sleep 2s

# Go back up to pick a favorite
Up
Up
Up
Sleep 2s

# Apply the theme
Enter
Sleep 2s

# Done
Ctrl+C
Sleep 1s
