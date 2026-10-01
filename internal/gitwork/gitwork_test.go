package gitwork_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/StevenACoffman/benchgate/internal/gitwork"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// testRepo builds a throwaway repository with two commits: the first writes
// marker.txt saying "base", the second changes it to "head". The marker is how
// a test proves which revision a worktree actually holds.
//
// It returns the repository root. t.TempDir removes it.
func testRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	dir := t.TempDir()

	runGit(t, dir, "init", "--quiet", "--initial-branch=main")
	// Identity and signing are configured per repository rather than through
	// the environment: forbidigo bans t.Setenv because it disables t.Parallel,
	// and a repository-local config cannot leak into another test.
	runGit(t, dir, "config", "user.email", "benchgate@example.test")
	runGit(t, dir, "config", "user.name", "benchgate test")
	runGit(t, dir, "config", "commit.gpgsign", "false")

	writeFile(t, filepath.Join(dir, "marker.txt"), "base\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "--quiet", "-m", "add the marker")

	writeFile(t, filepath.Join(dir, "marker.txt"), "head\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "--quiet", "-m", "change the marker")

	return dir
}

func TestResolve(t *testing.T) {
	t.Parallel()
	dir := testRepo(t)
	repo := gitwork.Repo{Dir: dir}

	head, err := repo.Resolve(t.Context(), "HEAD")
	ok(t, err)
	equals(t, 40, len(head))

	base, err := repo.Resolve(t.Context(), "HEAD~1")
	ok(t, err)
	assert(t, base != head, "HEAD~1 should not resolve to HEAD")

	// A branch name and a SHA must resolve to the same commit.
	byBranch, err := repo.Resolve(t.Context(), "main")
	ok(t, err)
	equals(t, head, byBranch)
}

// A missing revision in CI is nearly always a shallow clone, so the error says
// so rather than passing git's own message through.
func TestResolveReportsAMissingRevision(t *testing.T) {
	t.Parallel()
	dir := testRepo(t)
	repo := gitwork.Repo{Dir: dir}

	_, err := repo.Resolve(t.Context(), "origin/does-not-exist")
	assert(t, err != nil, "an unknown revision should be an error")
	assert(t, errors.Is(err, gitwork.ErrNoRevision),
		"the error should wrap ErrNoRevision so a caller can tell it apart")
	assert(t, strings.Contains(err.Error(), "fetch-depth"),
		"the message should name the fix: "+err.Error())
}

func TestCommitMessage(t *testing.T) {
	t.Parallel()
	dir := testRepo(t)
	repo := gitwork.Repo{Dir: dir}

	msg, err := repo.CommitMessage(t.Context(), "HEAD")
	ok(t, err)
	equals(t, "change the marker", msg)
}

// The central promise: the base revision is materialised somewhere else, so the
// caller's working tree is untouched — which is what makes the gate safe to run
// on a dirty checkout, unlike `git checkout` or `git reset --hard`.
func TestAtLeavesTheWorkingTreeAlone(t *testing.T) {
	t.Parallel()
	dir := testRepo(t)
	repo := gitwork.Repo{Dir: dir}

	// Uncommitted work, of exactly the kind cob's `git reset --hard` destroys.
	writeFile(t, filepath.Join(dir, "marker.txt"), "uncommitted work\n")
	writeFile(t, filepath.Join(dir, "untracked.txt"), "also uncommitted\n")

	var sawInWorktree string
	ok(t, repo.At(t.Context(), "HEAD~1", func(worktree string) error {
		sawInWorktree = readFile(t, filepath.Join(worktree, "marker.txt"))
		return nil
	}))

	equals(t, "base\n", sawInWorktree)
	equals(t, "uncommitted work\n", readFile(t, filepath.Join(dir, "marker.txt")))
	equals(t, "also uncommitted\n", readFile(t, filepath.Join(dir, "untracked.txt")))
}

func TestAtRemovesTheWorktreeAfterwards(t *testing.T) {
	t.Parallel()
	dir := testRepo(t)
	repo := gitwork.Repo{Dir: dir}

	var path string
	ok(t, repo.At(t.Context(), "HEAD~1", func(worktree string) error {
		path = worktree
		// Build output is what makes `git worktree remove` need --force.
		writeFile(t, filepath.Join(worktree, "leftover.o"), "artifact\n")
		return nil
	}))

	_, err := os.Stat(path)
	assert(t, os.IsNotExist(err), "the worktree should be gone after At returns")

	// And git must no longer have it registered, or the next run cannot reuse
	// the same path.
	out := runGit(t, dir, "worktree", "list")
	assert(t, !strings.Contains(out, path),
		"git should no longer list the worktree: "+out)
}

// A nested module is the motivating case: `benchgate check --dir _example` must
// benchmark _example on both sides, not the repository root.
func TestAtReproducesANestedDirectory(t *testing.T) {
	t.Parallel()
	dir := testRepo(t)
	requireGit(t)

	nested := filepath.Join(dir, "nested")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatalf("creating the nested directory: %s", err)
	}
	writeFile(t, filepath.Join(nested, "inside.txt"), "nested\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "--quiet", "-m", "add a nested directory")

	repo := gitwork.Repo{Dir: nested}
	var got string
	ok(t, repo.At(t.Context(), "HEAD", func(worktree string) error {
		got = readFile(t, filepath.Join(worktree, "inside.txt"))
		return nil
	}))
	equals(t, "nested\n", got)
}

func TestAtReturnsTheCallbackError(t *testing.T) {
	t.Parallel()
	dir := testRepo(t)
	repo := gitwork.Repo{Dir: dir}

	sentinel := errors.New("the callback failed")
	err := repo.At(t.Context(), "HEAD~1", func(string) error { return sentinel })
	assert(t, errors.Is(err, sentinel),
		"the callback's error should reach the caller unchanged")
}

// The worktree must still be removed when the callback fails, or a failed run
// leaves the repository with a registered worktree the next run collides with.
func TestAtCleansUpAfterAFailedCallback(t *testing.T) {
	t.Parallel()
	dir := testRepo(t)
	repo := gitwork.Repo{Dir: dir}

	var path string
	_ = repo.At(t.Context(), "HEAD~1", func(worktree string) error {
		path = worktree
		return errors.New("boom")
	})

	_, err := os.Stat(path)
	assert(t, os.IsNotExist(err), "the worktree should be removed even on failure")
}

func TestAtRejectsAnUnknownRef(t *testing.T) {
	t.Parallel()
	dir := testRepo(t)
	repo := gitwork.Repo{Dir: dir}

	called := false
	err := repo.At(t.Context(), "no-such-ref", func(string) error {
		called = true
		return nil
	})
	assert(t, err != nil, "an unknown ref should be an error")
	assert(t, !called, "the callback should not run when the worktree cannot be created")
}

func TestResolveHonoursContextCancellation(t *testing.T) {
	t.Parallel()
	dir := testRepo(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	repo := gitwork.Repo{Dir: dir}
	_, err := repo.Resolve(ctx, "HEAD")
	assert(t, err != nil, "a cancelled context should abort the command")
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	// G204: the arguments are literals written in this file, not input.
	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // G204, see above
	cmd.Dir = dir
	// An explicit environment keeps a developer's global git config — a signing
	// key, a commit template, a hooks path — out of the test.
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=benchgate test",
		"GIT_AUTHOR_EMAIL=benchgate@example.test",
		"GIT_COMMITTER_NAME=benchgate test",
		"GIT_COMMITTER_EMAIL=benchgate@example.test",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %s", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %s", path, err)
	}
	return string(b)
}

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
}

func assert(t *testing.T, condition bool, msg string) {
	t.Helper()
	if !condition {
		t.Fatal(msg)
	}
}

func equals(t *testing.T, exp, act any) {
	t.Helper()
	if exp != act {
		t.Fatalf("expected %v, got %v", exp, act)
	}
}
