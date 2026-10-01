default:
    @just --list

# Install the toolchains mise pins, then the git hooks
setup:
    mise install
    just hooks

# Install the lefthook git hooks (pre-commit, pre-push, commit-msg)
hooks:
    lefthook install

# Build the benchgate binary into ./bin
build:
    go build -o bin/benchgate .

# Run the unit suite with the race detector
test:
    go test -race -count=1 ./...

# Run the unit suite with a coverage report (a report for humans, not a gate)
test-cover:
    go test -race -coverprofile=coverage.out -covermode=atomic ./...
    go tool cover -func=coverage.out | tail -1
    @echo "HTML report: go tool cover -html=coverage.out"

# Refresh the committed golden files for the renderers, then show the diff to review
update-golden:
    go test ./internal/benchgate/ -update
    git diff --stat -- internal/benchgate/testdata

# Run golangci-lint over the repository
lint:
    golangci-lint run ./...

# Auto-fix what the linters can fix, then report what is left
lint-fix:
    golangci-lint run --fix ./...

# Verify go.mod/go.sum are tidy (CI runs this; a dirty tree fails the build)
tidy-check:
    #!/usr/bin/env bash
    set -euo pipefail
    # $TMPDIR is not set on every Linux runner, so do not rely on it.
    backup=$(mktemp -d)
    trap 'rm -rf "$backup"' EXIT
    cp go.mod "$backup/go.mod" && cp go.sum "$backup/go.sum"
    go mod tidy
    if ! diff -q "$backup/go.mod" go.mod >/dev/null || ! diff -q "$backup/go.sum" go.sum >/dev/null; then
        echo "go.mod/go.sum are not tidy; run 'go mod tidy' and commit the result" >&2
        exit 1
    fi
    echo "go.mod and go.sum are tidy"

# Mutation testing asks what coverage cannot: not "did a test execute this line?"
# but "would any test have noticed if it behaved differently?". Scoped to the
# pure core, because the shells are proven by the subprocess and end-to-end
# suites, which a mutation run does not execute.
#
# --config is passed explicitly rather than left to discovery. Whether mutago
# finds a `.mutago.yaml` on its own was not something we could establish: it
# runs happily on a deliberately corrupt config file under either spelling, so
# a file it silently ignores is indistinguishable from one it honoured. Naming
# the path here replaces that question with a check, because a wrong path is a
# hard "Could not read config file" rather than a default-config run that looks
# exactly like a configured one.
#
# The threshold is 85 against a measured 88.89%, which leaves room for a change
# to add a line or two without turning the gate red on arrival. 100% is not
# reachable: the tail is equivalent mutants, which alter the source without
# altering behaviour and so cannot be killed by any test. `sign(0)` is one —
# percentChange only calls it with a non-zero argument, so `f < 0` and `f <= 0`
# are the same function. strconv.ParseFloat's bitSize is another: anything that
# is not 32 behaves as 64.
#
# Mutation-test the pure core; fails below the threshold (exit code 4)
mutate threshold="85":
    mutago --config=.mutago.yaml --min-msi={{threshold}} --quiet --no-diffs ./internal/benchgate/

# Mutation-test the pure core and show the diff for every surviving mutant
mutate-report:
    mutago --config=.mutago.yaml --html-output ./internal/benchgate/
    @echo "wrote mutago-report.html"

# Format and lint the Markdown, then check the prose. vale is advisory here:
# its local verdict depends on whatever global config the developer has, and a
# gate that answers differently per machine is not one worth failing a build on.
docs:
    rumdl fmt README.md _example/README.md
    rumdl check README.md _example/README.md
    -vale README.md _example/README.md

# --strict-collection is the flag that matters here. Without it, a workflow
# zizmor cannot parse produces a WARN and is skipped, so a broken file reads as
# a clean audit. A duplicate `name:` key once hid this repository's entire CI
# workflow from the audit that way.
#
# Lint the workflows for validity (actionlint) and for safety (zizmor)
actions:
    actionlint
    zizmor --strict-collection .

# Run the govulncheck vulnerability scanner
vulncheck:
    govulncheck ./...

# Build, lint and test the nested _example module. The leading underscore keeps
# the Go tool from walking into it from the root module, so it needs its own
# invocation — it is never covered by `./...` above.
example:
    cd _example && go build ./... && go test -count=1 ./... && golangci-lint run ./...

# Dogfood: gate _example against its own git history. This is the same command
# CI runs, and the reason the CI path cannot rot unnoticed.
example-gate base="HEAD~1": build
    ./bin/benchgate check --dir _example --base {{base}} --format text

# Every gate CI runs on a pull request
check: tidy-check lint test vulncheck actions example
