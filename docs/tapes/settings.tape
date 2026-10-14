# Shared look for every docs tape. Each tape does `Source settings.tape`
# right after its Output lines; docs/tapes/record.sh renders them.
#
# FontSize 24 at Width 1600 with Padding 32 gives a 100-column terminal.
# The pages show the videos about 700 px wide, so 100 columns keeps the
# text readable; table output (at most 99 columns) fits on one line, and
# only the longest commands wrap. Keep new output under 100 columns, for
# example with --fields.
#
# Height 560 gives about 16 rows (about 29 px per row, plus the padding and
# window bar). Each tape that needs a different height sets `Set Height`
# right after `Source`, sized to its tallest scene plus a spare row, so the
# videos don't carry empty space. Live recordings (search, tail) get more
# headroom: real events can wrap differently from the mock's.
#
Set Shell "bash"
Set FontSize 24
Set Width 1600
Set Height 560
Set Padding 32
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 40ms
Set Framerate 24
Set CursorBlink false
