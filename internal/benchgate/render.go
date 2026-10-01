package benchgate

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// The supported Format values.
const (
	// FormatText is the aligned, human-readable form for a terminal.
	FormatText Format = "text"
	// FormatMarkdown is for a GitHub step summary or a pull-request comment.
	FormatMarkdown Format = "markdown"
	// FormatJSON is for another program. Its shape is the documented contract;
	// the other two formats are not.
	FormatJSON Format = "json"
)

// Format names an output encoding.
type Format string

// ParseFormat converts a flag value into a Format.
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(strings.TrimSpace(s))); f {
	case FormatText, FormatMarkdown, FormatJSON:
		return f, nil
	default:
		return "", Errorf(EINVALID, "unknown format %q: want text, markdown or json", s)
	}
}

// Render writes r to w in the given format.
func Render(w io.Writer, r *Report, f Format) error {
	switch f {
	case FormatText:
		return RenderText(w, r)
	case FormatMarkdown:
		return RenderMarkdown(w, r)
	case FormatJSON:
		return RenderJSON(w, r)
	default:
		return Errorf(EINVALID, "unknown format %q", string(f))
	}
}

// RenderText writes the report as aligned plain text.
func RenderText(w io.Writer, r *Report) error {
	var b strings.Builder
	writeTextHeader(&b, r)
	writeTextComparisons(&b, r)
	writeTextWarnings(&b, r)
	writeTextGaps(&b, r)
	writeTextSummary(&b, r)
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("writing text report: %w", err)
	}
	return nil
}

// RenderMarkdown writes the report as GitHub-flavoured Markdown.
func RenderMarkdown(w io.Writer, r *Report) error {
	var b strings.Builder
	b.WriteString("## benchgate\n\n")
	writeMarkdownProvenance(&b, r)
	writeMarkdownComparisons(&b, r)
	writeMarkdownWarnings(&b, r)
	writeMarkdownGaps(&b, r)
	writeMarkdownSummary(&b, r)
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("writing markdown report: %w", err)
	}
	return nil
}

// RenderJSON writes the report as indented JSON.
//
// This encoding is the machine contract: field names are stable, every
// comparison carries both the p-value and the delta so a consumer can apply its
// own thresholds, and the exit code the policy implies is included so a consumer
// need not reimplement the decision.
func RenderJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(newReportJSON(r)); err != nil {
		return fmt.Errorf("writing json report: %w", err)
	}
	return nil
}

// ---------- text ----------

func writeTextHeader(b *strings.Builder, r *Report) {
	if r.ComparesRevisions() {
		fmt.Fprintf(b, "benchmark gate: %s -> %s\n", r.Base.String(), r.Head.String())
	} else {
		b.WriteString("benchmark coverage\n")
	}
	if r.BenchCommand != "" {
		fmt.Fprintf(b, "command:  %s\n", r.BenchCommand)
	}
	// Whatever every row shares is stated once, here, so the table can spend
	// its width on the numbers.
	if pkg := r.Scope().Pkg; pkg != "" {
		fmt.Fprintf(b, "package:  %s\n", pkg)
	}
	if r.Platform != "" {
		fmt.Fprintf(b, "platform: %s\n", r.Platform)
	}
	if r.CPU != "" {
		fmt.Fprintf(b, "cpu:      %s\n", r.CPU)
	}
	fmt.Fprintf(
		b,
		"policy:   alpha %.3g, tolerance %.3g%%\n",
		r.policy.Alpha(),
		r.policy.Tolerance(),
	)
	for _, n := range r.Notices {
		fmt.Fprintf(b, "notice:   %s\n", n)
	}
}

func writeTextComparisons(b *strings.Builder, r *Report) {
	if len(r.Comparisons) == 0 {
		if r.ComparesRevisions() {
			b.WriteString("\nno benchmarks compared\n")
		}
		return
	}
	b.WriteString("\n")
	// tabwriter buffers until Flush, so the intermediate writes cannot fail in
	// a way worth reacting to; Flush is where a real write error would surface,
	// and the destination here is a strings.Builder, which never fails.
	scope := r.Scope()
	tw := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "BENCHMARK\tUNIT\tBASE\tHEAD\tDELTA\tP\tN\tVERDICT")
	for i := range r.Comparisons {
		c := &r.Comparisons[i]
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			scope.Label(c), c.Unit, c.BaseCell(), c.HeadCell(),
			c.DeltaString(), c.PString(), c.NString(), c.Verdict)
	}
	_ = tw.Flush()
}

// writeTextWarnings prints what the statistics wanted to say. Collecting
// warnings and showing them only in the JSON would hide exactly the cases where
// a verdict should not be acted on from the reader most likely to act on it.
func writeTextWarnings(b *strings.Builder, r *Report) {
	warnings := r.Warnings()
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintf(b, "\nwarnings (%d):\n", len(warnings))
	for _, w := range warnings {
		fmt.Fprintf(b, "  %s: %s\n", w.Subject, w.Detail)
	}
}

func writeTextGaps(b *strings.Builder, r *Report) {
	if len(r.Gaps) == 0 {
		return
	}
	fmt.Fprintf(b, "\nbenchmark coverage gaps (%d):\n", len(r.Gaps))
	for i := range r.Gaps {
		fmt.Fprintf(b, "  %s\n", r.Gaps[i].String())
	}
}

func writeTextSummary(b *strings.Builder, r *Report) {
	counts := r.Verdicts()
	b.WriteString("\n")
	if prof := r.Coverage; prof.HasTotal {
		fmt.Fprintf(b, "benchmark statement coverage: %.1f%%\n", prof.TotalPercent)
	}
	fmt.Fprintf(
		b,
		"%d regressed, %d improved, %d unchanged, %d unmeasurable, %d added, %d removed\n",
		counts[VerdictRegressed],
		counts[VerdictImproved],
		counts[VerdictUnchanged],
		counts[VerdictUnmeasurable],
		counts[VerdictAdded],
		counts[VerdictRemoved],
	)
	fmt.Fprintf(b, "exit code: %d\n", r.ExitCode())
}

// ---------- markdown ----------

func writeMarkdownProvenance(b *strings.Builder, r *Report) {
	if r.ComparesRevisions() {
		fmt.Fprintf(b, "`%s` → `%s`\n\n", r.Base.String(), r.Head.String())
	}
	if pkg := r.Scope().Pkg; pkg != "" {
		fmt.Fprintf(b, "`%s`\n\n", pkg)
	}
	if r.Platform != "" || r.CPU != "" {
		fmt.Fprintf(b, "%s%s\n\n", r.Platform, markdownCPUSuffix(r.CPU))
	}
	if r.BenchCommand != "" {
		fmt.Fprintf(b, "```\n%s\n```\n\n", r.BenchCommand)
	}
	for _, n := range r.Notices {
		fmt.Fprintf(b, "> %s\n\n", n)
	}
}

func writeMarkdownComparisons(b *strings.Builder, r *Report) {
	if len(r.Comparisons) == 0 {
		if r.ComparesRevisions() {
			b.WriteString("No benchmarks were compared.\n\n")
		}
		return
	}
	scope := r.Scope()
	b.WriteString("| Benchmark | Unit | Base | Head | Delta | p | n | Verdict |\n")
	b.WriteString("| --- | --- | --: | --: | --: | --: | --: | --- |\n")
	for i := range r.Comparisons {
		c := &r.Comparisons[i]
		fmt.Fprintf(b, "| `%s` | %s | %s | %s | %s | %s | %s | %s |\n",
			scope.Label(c), c.Unit, c.BaseCell(), c.HeadCell(),
			c.DeltaString(), c.PString(), c.NString(), markdownVerdict(c.Verdict))
	}
	b.WriteString("\n")
}

func writeMarkdownWarnings(b *strings.Builder, r *Report) {
	warnings := r.Warnings()
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintf(b, "<details><summary>Warnings (%d)</summary>\n\n", len(warnings))
	for _, w := range warnings {
		fmt.Fprintf(b, "- `%s`: %s\n", w.Subject, w.Detail)
	}
	b.WriteString("\n</details>\n\n")
}

func writeMarkdownGaps(b *strings.Builder, r *Report) {
	if len(r.Gaps) == 0 {
		return
	}
	fmt.Fprintf(b, "<details><summary>Benchmark coverage gaps (%d)</summary>\n\n", len(r.Gaps))
	for i := range r.Gaps {
		fmt.Fprintf(b, "- %s\n", r.Gaps[i].String())
	}
	b.WriteString("\n</details>\n\n")
}

func writeMarkdownSummary(b *strings.Builder, r *Report) {
	counts := r.Verdicts()
	if prof := r.Coverage; prof.HasTotal {
		fmt.Fprintf(b, "Benchmark statement coverage: **%.1f%%**\n\n", prof.TotalPercent)
	}
	fmt.Fprintf(
		b,
		"**%d regressed**, %d improved, %d unchanged, %d unmeasurable, %d added, %d removed.\n",
		counts[VerdictRegressed],
		counts[VerdictImproved],
		counts[VerdictUnchanged],
		counts[VerdictUnmeasurable],
		counts[VerdictAdded],
		counts[VerdictRemoved],
	)
	if code := r.ExitCode(); code != ExitPass {
		fmt.Fprintf(b, "\nThis run fails the gate (exit %d).\n", code)
	}
}

func markdownCPUSuffix(cpu string) string {
	if cpu == "" {
		return ""
	}
	return " · " + cpu
}

func markdownVerdict(v Verdict) string {
	switch v {
	case VerdictRegressed:
		return "🔴 regressed"
	case VerdictImproved:
		return "🟢 improved"
	case VerdictUnchanged:
		return "~ unchanged"
	case VerdictUnmeasurable:
		return "⚠️ unmeasurable"
	case VerdictAdded:
		return "➕ added"
	case VerdictRemoved:
		return "➖ removed"
	default:
		return string(v)
	}
}

// String renders a Revision as "ref (sha)", shortening the SHA, and as just the
// ref when there is no commit behind it.
func (v Revision) String() string {
	if v.SHA == "" {
		return v.Ref
	}
	short := v.SHA
	const shortSHALen = 12
	if len(short) > shortSHALen {
		short = short[:shortSHALen]
	}
	if v.Ref == "" {
		return short
	}
	return v.Ref + " (" + short + ")"
}
