# From the repository root: task docs:demo
# Requires VHS 0.11.0, FFmpeg, ttyd, and the VHS default monospace font.
Output docs/demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 640
Set Padding 12
Set Theme "Catppuccin Mocha"
Set TypingSpeed 40ms
Set Framerate 10

Hide
Type "mise exec go -- go run ./internal/doctest/cmd/demo"
Enter
Sleep 4s
Type "4"
Sleep 1s
Show
Sleep 2s
Down 2
Sleep 1s
Enter
Sleep 2s
Type "q"
Sleep 500ms
Type "5"
Sleep 2s
Right
Sleep 2s
Type "?"
Sleep 2s
Type "status"
Sleep 2s
Escape
Sleep 500ms
Hide
Ctrl+C
Sleep 1s
