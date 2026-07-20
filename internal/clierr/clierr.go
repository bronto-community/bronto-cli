// Package clierr defines the CLI's typed errors: stable machine codes,
// human hints, and the exit-code contract (spec §5).
package clierr

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

type Error struct {
	Code      string
	Message   string
	Hint      string
	DocsURL   string
	Retryable bool
}

func New(code, message string) *Error { return &Error{Code: code, Message: message} }

func (e *Error) WithHint(h string) *Error { e.Hint = h; return e }
func (e *Error) WithDocs(u string) *Error { e.DocsURL = u; return e }
func (e *Error) WithRetryable() *Error    { e.Retryable = true; return e }
func (e *Error) Error() string            { return e.Message }

func (e *Error) ExitCode() int {
	switch {
	case strings.HasPrefix(e.Code, "usage_"), strings.HasPrefix(e.Code, "config_"):
		return 2
	case strings.HasPrefix(e.Code, "auth_"):
		return 3
	case strings.HasSuffix(e.Code, "_not_found"):
		return 4
	case e.Code == "rate_limited", e.Code == "timeout":
		return 5
	default:
		return 1
	}
}

// ExitCode maps any error to the exit-code contract.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ce *Error
	if ok := asCLIError(err, &ce); ok {
		return ce.ExitCode()
	}
	return 1
}

func asCLIError(err error, target **Error) bool {
	return errors.As(err, target)
}

// Render writes err to w. machineMode selects the stable JSON envelope.
func Render(w io.Writer, err error, machineMode bool) {
	var ce *Error
	if !asCLIError(err, &ce) {
		ce = New("unknown_error", err.Error())
	}
	if machineMode {
		inner := map[string]any{
			"code": ce.Code, "message": ce.Message, "retryable": ce.Retryable,
		}
		// Remediation must reach machine consumers too — agents acting on
		// the envelope were losing "run 'bronto auth login'"-class hints
		// that only the human renderer printed. Additive, so the envelope
		// contract stays backward-compatible.
		if ce.Hint != "" {
			inner["hint"] = ce.Hint
		}
		env := map[string]any{"error": inner}
		b, _ := json.Marshal(env)
		_, _ = fmt.Fprintln(w, string(b))
		return
	}
	_, _ = fmt.Fprintf(w, "Error: %s (%s)\n", ce.Message, ce.Code)
	if ce.Hint != "" {
		_, _ = fmt.Fprintf(w, "Hint: %s\n", ce.Hint)
	}
	if ce.DocsURL != "" {
		_, _ = fmt.Fprintf(w, "Docs: %s\n", ce.DocsURL)
	}
}
