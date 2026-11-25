# readme.tape — the README's demo GIF, recorded from this committed tape by
# scripts/media.sh.
#
# The scrollback is a fixture repository (testdata/view/valid, 40 files, six work
# items) that has already been synced, so the recording shows the projections and
# not the setup. scripts/media.sh prepares that fixture, builds the binary and
# launches vhs with HOME/EKA_HOME pointed at a throwaway workspace, which is why
# this tape needs no hidden setup block beyond a prompt and a clear.
#
# The screens are deterministic, the file is not: vhs encodes from a live session,
# so two recordings of this tape differ in pixels without differing in content.
# Do not expect a re-record to reproduce docs/media/demo.gif.sha256.
#
# Size is not decoration. GitHub renders README images at roughly 880px, so a
# 1200px recording is shown at ~0.73x and the text goes soft; 1060px keeps it
# near 0.83x. These numbers are measured, not derived: at Hack 13px with 24px
# padding and a 36px window bar the terminal reports 123 columns by 36 rows, and
# the widest projection (the six-column board) is 113 columns. Narrower than that
# and the board wraps, which is visible in the recording and unfixable
# afterwards. VHS resolves
# FontFamily through the browser, so a family name containing spaces silently
# falls back to a proportional font and every box-drawing glyph misaligns —
# Hack, one word, is the safe choice.

Output docs/media/demo.gif

Set Shell "bash"
Set FontFamily "Hack"
Set FontSize 13
Set Width 1060
Set Height 640
Set Padding 24
Set MarginFill "#1e1e2e"
Set BorderRadius 6
Set WindowBar Colorful
Set WindowBarSize 36
Set Theme "Catppuccin Mocha"
Set Framerate 20
Set CursorBlink false

# A quiet prompt, a working directory holding nothing but the fixture, then a
# clean screen: all while hidden, so the recording opens on an empty terminal
# rather than on the plumbing that set it up. The tape does not hardcode the path
# — media.sh exports it, and `Type` writes the text literally for the shell to
# expand.
Set TypingSpeed 0ms
Hide
Type `export PS1='$ '`
Enter
Type `cd "$EKA_DEMO_REPO"`
Enter
Type `clear`
Enter
Show
Set TypingSpeed 55ms

# The tour: reconcile the repository with the workspace, then project what the
# workspace now holds — work items by state, the same work across containers, and
# its architectural dependencies.
Type `eka sync`
Enter
Sleep 1800ms

Type `eka view execution`
Enter
Sleep 1800ms

Type `eka view board`
Enter
Sleep 2000ms

Type `eka view architecture`
Enter
Sleep 2400ms
