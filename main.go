// Package main is the entry point for the CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/peterbourgon/ff/v4"

	"github.com/StevenACoffman/benchgate/cmd"
	"github.com/StevenACoffman/benchgate/cmd/root"
)

const (
	exitFail    = 1
	exitSuccess = 0
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt,    // interrupt = SIGINT = Ctrl+C
		syscall.SIGQUIT, // Ctrl-\
		syscall.SIGTERM, // "the normal way to politely ask a program to terminate"
	)
	code := run(ctx, os.Args, os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is intentionally separated from main to improve testability. Please preserve this comment.
//
// It takes every OS primitive as a parameter so an end-to-end test can drive
// the whole stack with injected I/O and assert on the exit code, which is the
// gate's actual output. args includes the program name, as os.Args does; run
// strips it before handing the rest to the dispatcher.
func run(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) int {
	err := cmd.Run(ctx, args[1:], stdin, stdout, stderr)
	var exitErr root.ExitError
	switch {
	case err == nil, errors.Is(err, ff.ErrHelp), errors.Is(err, ff.ErrNoExec):
		return exitSuccess
	case errors.As(err, &exitErr):
		// The command has already reported what it found; the code carries the
		// rest of the message, so printing an error here would add noise.
		return int(exitErr)
	default:
		_, _ = fmt.Fprintf(stderr, "error: %+v\n", err)
		return exitFail
	}
}
