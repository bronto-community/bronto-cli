# Shared look for every docs tape. Each tape does `Source settings.tape`
# right after its Output lines; docs/tapes/record.sh renders them.
#
# Height 420 gives a 17-row terminal (about 20 px per row), enough for every
# tape but two. A tape whose tallest scene needs more rows sets its own
# `Set Height` right after `Source`: search (24 rows, 580) and resources
# (18 rows, 480). VHS text output reads the buffer, not the screen, so a
# scene that scrolls just loses its bottom rows in the golden; tapegolden
# catches that, because a settled scene must end at a prompt, and fails the
# render with "doesn't end at a prompt".
Set Shell "bash"
Set FontSize 16
Set Width 1280
Set Height 420
Set Padding 24
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 40ms
Set Framerate 24
Set CursorBlink false
