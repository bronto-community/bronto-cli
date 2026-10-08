# Shared look for every docs tape. Each tape does `Source settings.tape`
# right after its Output lines; docs/tapes/record.sh renders them.
#
# Height 420 gives a 17-row terminal (about 20 px per row). A tape that
# needs more sets its own `Set Height` right after `Source`: resources
# (18-row scene, 480) and search (16 rows, but 480 for headroom, because its
# long -x command wraps and live data can wrap differently).
#
# Live recordings run against real events, which can be taller than the
# mock's: bound what a scene prints where you can (`-x --fields ...` fixes
# the field count, though -x never truncates values, so a long or multi-line
# value can still add rows), and leave headroom. VHS text output reads the
# buffer, not the screen, so a scene that scrolls just loses its bottom rows
# in the golden; tapegolden catches that, because a settled scene must end
# at a prompt, and fails the render with "doesn't end at a prompt".
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
