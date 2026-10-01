package gate

import (
	"fmt"
	"io"
	"os"

	"github.com/StevenACoffman/benchgate/internal/benchgate"
)

// Emit writes a report where a CI job needs it.
//
// The report goes to out in the requested format. When summaryPath is not
// empty, the Markdown rendering is also appended to that file, whatever the
// format on out is: the two destinations have different readers. A job log is
// read by whoever is debugging, and plain text is easier there; a GitHub step
// summary or a pull-request comment is read by a reviewer, and Markdown renders
// as a table for them. Making the operator choose one would mean losing the
// other.
//
// Appending rather than truncating is what the GitHub step summary file
// expects: several steps contribute to one summary, and truncating would
// discard whatever ran earlier.
//
// Ensures: out is written before summaryPath, so a failure to open the summary
// file cannot swallow the report a reader was going to see anyway.
func Emit(
	out io.Writer,
	summaryPath string,
	report *benchgate.Report,
	format benchgate.Format,
) error {
	if err := benchgate.Render(out, report, format); err != nil {
		return fmt.Errorf("gate.Emit: %w", err)
	}
	if summaryPath == "" {
		return nil
	}
	f, err := os.OpenFile(summaryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("gate.Emit: opening the summary file: %w", err)
	}
	if err = benchgate.RenderMarkdown(f, report); err != nil {
		_ = f.Close()
		return fmt.Errorf("gate.Emit: writing the summary file: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("gate.Emit: closing the summary file: %w", err)
	}
	return nil
}
