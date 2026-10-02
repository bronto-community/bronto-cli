package docsnippets

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/creack/pty"
)

// runOnPTY runs cmd with its stdout on the slave side of a fresh
// pseudo-terminal and copies everything written there into stdout. That
// is what a reader's terminal gives bronto: isatty(stdout) is true, so the
// CLI picks its interactive defaults (table output, TTY-only hints) by
// its own detection, not by a flag the docs would have to show.
//
// Only stdout is the terminal. stdin stays /dev/null, so commands that
// would prompt (confirmations, auth login) refuse instead of hanging, and
// stderr stays a pipe for the failure report. The terminal size comes
// from COLUMNS/LINES in env. The line discipline's "\n" -> "\r\n"
// translation is undone so expected lines compare as plain text.
func runOnPTY(cmd *exec.Cmd, stdout *bytes.Buffer, env []string) error {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return fmt.Errorf("open pseudo-terminal: %w", err)
	}
	defer func() { _ = ptmx.Close() }()
	if err := pty.Setsize(ptmx, &pty.Winsize{Cols: envUint16(env, "COLUMNS", 100), Rows: envUint16(env, "LINES", 40)}); err != nil {
		_ = tty.Close()
		return fmt.Errorf("size pseudo-terminal: %w", err)
	}
	cmd.Stdout = tty
	if err := cmd.Start(); err != nil {
		_ = tty.Close()
		return err
	}
	// The child holds its own copy of the slave; closing ours means the
	// master reads EOF/EIO once the child (and anything it spawned) exits.
	_ = tty.Close()
	var raw bytes.Buffer
	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(&raw, ptmx) // ends with EIO (Linux) or EOF (macOS) at hangup
		close(copied)
	}()
	waitErr := cmd.Wait()
	<-copied
	stdout.WriteString(strings.ReplaceAll(raw.String(), "\r\n", "\n"))
	return waitErr
}

func envUint16(env []string, key string, def uint16) uint16 {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			if n, err := strconv.ParseUint(v, 10, 16); err == nil {
				return uint16(n)
			}
		}
	}
	return def
}
