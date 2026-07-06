package output

// ColorEnabled implements the precedence
// --no-color flag > NO_COLOR > FORCE_COLOR > TERM=dumb > TTY (spec §5).
func ColorEnabled(noColorFlag, isTTY bool, getenv func(string) string) bool {
	if noColorFlag {
		return false
	}
	if getenv("NO_COLOR") != "" {
		return false
	}
	if getenv("FORCE_COLOR") != "" {
		return true
	}
	if getenv("TERM") == "dumb" {
		return false
	}
	return isTTY
}
