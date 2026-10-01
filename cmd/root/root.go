// Package root defines the root configuration for the CLI.
package root

import (
	"fmt"
	"io"

	"github.com/peterbourgon/ff/v4"
)

// ExitError is returned by commands that want a specific non-zero exit code
// without printing an additional error message. run() in main.go checks for
// ExitError with errors.As and calls os.Exit(int(e)) directly, bypassing the
// default "error: ..." printer.
type ExitError int

// Config holds shared I/O writers, the flags every subcommand inherits, and the
// root ff.Command. All subcommand configs embed *Config to inherit these.
type Config struct {
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Flags   *ff.FlagSet
	Command *ff.Command

	// Dir is the module directory every subcommand operates in. It is shared
	// because all three subcommands need it and a reader should not have to
	// remember which spelling each one uses.
	Dir string
	// Verbose sends every subprocess command line and its stderr to Stderr.
	// Without it a failed `go test` inside the gate is a bare exit status, and
	// the first thing anyone debugging a CI failure needs is the command.
	Verbose bool
}

// New returns a new root Config with the given I/O writers.
func New(stdin io.Reader, stdout, stderr io.Writer) *Config {
	var cfg Config
	cfg.Stdin = stdin
	cfg.Stdout = stdout
	cfg.Stderr = stderr
	cfg.Flags = ff.NewFlagSet("benchgate")
	cfg.Flags.StringVar(&cfg.Dir, 0, "dir", ".",
		"module directory to operate in")
	cfg.Flags.BoolVar(&cfg.Verbose, 'v', "verbose",
		"write every subprocess command line and its stderr to stderr")
	cfg.Command = &ff.Command{
		Name:      "benchgate",
		Usage:     "benchgate <SUBCOMMAND> ...",
		ShortHelp: "gate a pull request on benchmark regressions and benchmark coverage gaps",
		LongHelp: `benchgate measures Go benchmarks at two revisions, decides whether any
difference is both statistically significant and large enough to matter, and
reports which code no benchmark reaches.

Subcommands:

  check      measure a base revision and HEAD, compare them, and report gaps
  compare    compare two benchmark result files that already exist
  gaps       report benchmark coverage gaps without comparing anything
  version    print build and version information

Exit codes:

  0  every enabled gate was satisfied
  1  the run could not complete (git, the go command, or bad input)
  2  a regression was found and --fail-on-regression was set
  3  a coverage gap was found and --fail-on-gap was set

Nothing blocks unless asked: --fail-on-regression and --fail-on-gap are off by
default, so a project can see what the gate reports before it starts failing
builds.

Every flag can also be set by a BENCHGATE_-prefixed environment variable:
prepend BENCHGATE_, uppercase, and replace dashes with underscores, so
--tolerance becomes BENCHGATE_TOLERANCE. Flags given on the command line win.`,
		Flags: cfg.Flags,
	}
	return &cfg
}

// DebugWriter returns the writer subprocess diagnostics should go to: Stderr
// when --verbose is set, nil to discard otherwise.
//
// Returning nil rather than io.Discard is deliberate: the adapters check for
// nil and skip formatting entirely, so an unverbose run does not pay to build
// strings nobody reads.
func (c *Config) DebugWriter() io.Writer {
	if c.Verbose {
		return c.Stderr
	}
	return nil
}

func (e ExitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
