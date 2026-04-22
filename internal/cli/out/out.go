// Package out provides CLI output utilities: styled text, spinners, and progress bars.
//
// Two packages are used deliberately:
//   - github.com/briandowns/spinner handles animated spinners. It runs its own goroutine,
//     tracks the exact width of its last output, and erases cleanly. Using schollz/progressbar
//     in indeterminate mode (-1) as a spinner caused jitter because progressbar recalculates
//     variable-width fields (elapsed time, percentage) on every render, leaving stale characters
//     that \r does not fully overwrite.
//   - github.com/schollz/progressbar/v3 handles determinate progress bars, which is exactly
//     what it was designed for.
package out

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/briandowns/spinner"
	"github.com/fatih/color"
	"github.com/schollz/progressbar/v3"
)

// Writer is the output destination for all CLI output.
// All output goes to stderr so stdout stays clean for machine-readable data (e.g. orb export).
var Writer io.Writer = os.Stderr

var (
	green  = color.New(color.FgGreen)
	yellow = color.New(color.FgYellow)
	red    = color.New(color.FgRed)
)

// isTTY reports whether output is an interactive terminal.
// color.NoColor is set by fatih/color on init via go-isatty.
func isTTY() bool { return !color.NoColor }

// Step prints a status line with an emoji prefix.
func Step(emoji, msg string) {
	fmt.Fprintf(Writer, "%s  %s\n", emoji, msg)
}

// Success prints a green success line.
func Success(msg string) {
	green.Fprintf(Writer, "✅  %s\n", msg)
}

// Warning prints a yellow warning line.
func Warning(msg string) {
	yellow.Fprintf(Writer, "⚠️   %s\n", msg)
}

// Fatal prints a red error line and exits.
func Fatal(msg string) {
	red.Fprintf(Writer, "❌  %s\n", msg)
	os.Exit(1)
}

// Infof prints an indented informational line.
func Infof(format string, a ...any) {
	fmt.Fprintf(Writer, "    "+format+"\n", a...)
}

// SpinnerHandle controls a running spinner.
type SpinnerHandle struct {
	s *spinner.Spinner
}

// Stop stops the spinner and prints a success line.
func (h *SpinnerHandle) Stop(finalMsg string) {
	if h.s != nil {
		fmt.Fprint(Writer, "\n") // freeze current frame, move to next line
		h.s.Stop()               // erase() clears the new blank line (no visible effect)
	}
	Success(finalMsg)
}

// Fail stops the spinner and prints a failure line.
func (h *SpinnerHandle) Fail(finalMsg string) {
	if h.s != nil {
		fmt.Fprint(Writer, "\n")
		h.s.Stop()
	}
	red.Fprintf(Writer, "❌  %s\n", finalMsg)
}

// Spinner starts a spinner with the given message.
// In non-TTY mode it prints a plain "→ msg" line with no animation.
func Spinner(msg string) *SpinnerHandle {
	if !isTTY() {
		fmt.Fprintf(Writer, "  → %s\n", msg)
		return &SpinnerHandle{}
	}
	s := spinner.New(spinner.CharSets[14], 80*time.Millisecond, spinner.WithWriter(Writer))
	s.Suffix = "  " + msg
	s.Start()
	return &SpinnerHandle{s: s}
}

// ProgressBar creates and returns a progress bar with the given total and description.
// Call bar.Add(n) to advance it. In non-TTY mode progressbar falls back to plain text.
func ProgressBar(total int, description string) *progressbar.ProgressBar {
	return progressbar.NewOptions(total,
		progressbar.OptionSetWriter(Writer),
		progressbar.OptionSetDescription(description),
		progressbar.OptionEnableColorCodes(true),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "[default]█[reset]",
			SaucerHead:    "[default]█[reset]",
			SaucerPadding: "░",
			BarStart:      "│",
			BarEnd:        "│",
		}),
		progressbar.OptionShowCount(),
		progressbar.OptionSetWidth(40),
		progressbar.OptionSetElapsedTime(false),
		progressbar.OptionSetPredictTime(false),
		progressbar.OptionOnCompletion(func() { fmt.Fprintln(Writer) }),
	)
}
