# From the repository root: task docs:demo
# Requires VHS 0.12.1, FFmpeg, ttyd, Git, and the VHS default monospace font.
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
Sleep 5s
Type "4"
Sleep 1s
Show
Sleep 2s
# BL-5 heads the todo group; its branch carries a stored tour.
Type "]"
Sleep 300ms
Type "j"
Sleep 1s
Enter
Sleep 2s
Tab@300ms 5
Sleep 2s
Enter
Sleep 1500ms
Tab@300ms 2
Sleep 3s
Type "]t"
Sleep 2s
Type "]t"
Sleep 3s
Backspace
Sleep 500ms
Tab
Sleep 2s
Type "!"
Sleep 2s
Escape
Sleep 500ms
Type "q"
Sleep 500ms
Type "5"
Sleep 2s
Right
Sleep 1500ms
Type ":"
Sleep 500ms
Type "status"
Sleep 2s
Escape
Sleep 500ms
Hide
Ctrl+C
Sleep 1s
