// Package gitwork reads a git repository and materialises a revision in a
// throwaway worktree.
//
// It is the adapter for one external dependency — the `git` command. The
// operation that matters is At: it checks out a base revision somewhere else
// entirely, so the caller's working tree is never touched. That is the whole
// reason this package exists rather than a `git checkout` in the gate.
package gitwork

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNoRevision reports a revision that does not exist in the repository. In
// CI this is almost always a shallow clone: the fix is fetch-depth: 0, and
// saying so is more useful than passing git's own message through.
var ErrNoRevision = errors.New(
	"revision not found in this repository; a shallow clone needs full history (fetch-depth: 0)")

// Repo is a git repository, addressed through a directory inside it.
//
// The zero Repo uses "git" from PATH against the process's working directory,
// so it is usable as-is.
type Repo struct {
	// GitCmd is the git binary to run. Empty means "git", resolved on PATH.
	GitCmd string
	// Dir is a directory inside the repository. It need not be the root: At
	// reproduces Dir's position relative to the root inside the worktree, so a
	// nested module is benchmarked at the matching path on both sides.
	Dir string
	// Debug receives each command line and the stderr of each command. Nil
	// discards both.
	Debug io.Writer
}

// Resolve turns a revision — a branch, a tag, "HEAD~1", a SHA — into its full
// commit hash.
//
// Returns an error wrapping ErrNoRevision when the revision does not exist, so
// a caller can tell "you asked for a ref that is not here" (usually a shallow
// clone in CI) from "git is broken".
func (r Repo) Resolve(ctx context.Context, rev string) (string, error) {
	out, err := r.run(ctx, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil || len(out) == 0 {
		return "", fmt.Errorf("resolving %q: %w", rev, ErrNoRevision)
	}
	return string(bytes.TrimSpace(out)), nil
}

// CommitMessage returns the full message of a revision, subject and body.
func (r Repo) CommitMessage(ctx context.Context, rev string) (string, error) {
	out, err := r.run(ctx, "log", "-1", "--format=%B", rev)
	if err != nil {
		return "", fmt.Errorf("reading the commit message of %q: %w", rev, err)
	}
	return string(bytes.TrimSpace(out)), nil
}

// At materialises ref in a temporary detached worktree and calls fn with the
// directory corresponding to r.Dir inside it.
//
// The caller's working tree is never modified, which is what makes this safe to
// run on a dirty checkout — unlike `git checkout` or `git reset --hard`, which
// would discard uncommitted work. The worktree is removed before At returns,
// whether fn succeeded or not.
//
// Requires: ref resolves in this repository.
// Ensures:  the directory passed to fn exists for the duration of the call and
// not afterwards; fn's error is returned unchanged, and a cleanup failure is
// reported alongside it rather than replacing it.
func (r Repo) At(ctx context.Context, ref string, fn func(dir string) error) (err error) {
	prefix, err := r.prefix(ctx)
	if err != nil {
		return err
	}
	// MkdirTemp, not a directory inside the repository: a worktree under the
	// repository would be walked by `./...` and picked up by `git status`.
	parent, err := os.MkdirTemp("", "benchgate-worktree-")
	if err != nil {
		return fmt.Errorf("creating a temporary directory: %w", err)
	}
	// `git worktree add` insists on a path that does not exist yet.
	tree := filepath.Join(parent, "tree")
	defer func() {
		err = errors.Join(err, r.removeWorktree(ctx, tree), os.RemoveAll(parent))
	}()

	if _, addErr := r.run(ctx,
		"worktree", "add", "--quiet", "--detach", tree, ref,
	); addErr != nil {
		return fmt.Errorf("adding a worktree at %q: %w", ref, addErr)
	}
	return fn(filepath.Join(tree, prefix))
}

// prefix is r.Dir's path relative to the repository root, so At can offer fn
// the matching directory inside the worktree. It is "" when Dir is the root.
func (r Repo) prefix(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "rev-parse", "--show-prefix")
	if err != nil {
		return "", fmt.Errorf("locating %q within its repository: %w", r.Dir, err)
	}
	// git reports a slash-separated prefix with a trailing slash, or nothing at
	// the root.
	return filepath.FromSlash(strings.TrimSuffix(string(bytes.TrimSpace(out)), "/")), nil
}

func (r Repo) removeWorktree(ctx context.Context, tree string) error {
	// --force because the benchmark run leaves build artifacts behind, and git
	// refuses to remove a worktree with untracked files without it. A context
	// that is already cancelled would fail the removal, so cleanup gets a fresh
	// one: leaving a worktree registered would break the next run.
	cleanupCtx := context.WithoutCancel(ctx)
	if _, err := r.run(cleanupCtx, "worktree", "remove", "--force", tree); err != nil {
		return fmt.Errorf("removing the worktree at %s: %w", tree, err)
	}
	return nil
}

func (r Repo) run(ctx context.Context, args ...string) ([]byte, error) {
	// The command name and the revision come from flags: the operator's own
	// input, naming revisions in the operator's own checkout.
	cmd := exec.CommandContext(ctx, r.gitCmd(), args...) //nolint:gosec // G204, see above
	cmd.Dir = r.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	r.debugf("+ %s %s\n", r.gitCmd(), strings.Join(args, " "))

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			r.debugf("%s", stderr.String())
		}
		return nil, fmt.Errorf("%s %s: %w\n%s",
			r.gitCmd(), strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (r Repo) gitCmd() string {
	if r.GitCmd == "" {
		return "git"
	}
	return r.GitCmd
}

// debugf writes a diagnostic line. A failed write to a diagnostic stream is
// not worth reacting to: there is nowhere better to report it, and failing the
// gate because the verbose log could not be written would be absurd.
func (r Repo) debugf(format string, args ...any) {
	if r.Debug == nil {
		return
	}
	_, _ = fmt.Fprintf(r.Debug, format, args...)
}
