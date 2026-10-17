# Contributing

Thank you for helping. This page is the process: setting up, checking a change, and getting it merged. How the code is written (layout, comments, errors, logging, listers, tests, licence headers) is in [docs/development.md](docs/development.md): read it before your first change.

## Workflow

- Branch from `main` and open the pull request against it.
- Name the branch after the change's type: `fix/sqs-pagination`, `feat/azure-scope`, `docs/catalogue`.
- Keep pull requests focused: one logical change each. A bug fix includes a test that fails without the fix.
- Pull requests are squash-merged, so the title becomes the commit on `main`: write it as a [Conventional Commit](#commit-messages).
- Say in the description what changes and why, how you tested it, and whether a sandbox should run (see [Sandbox](#sandbox)).
- A pull request merges once every check is green, the [CLA](#contributor-license-agreement) check included.

## Contributor License Agreement

Contributions are accepted under the [Contributor License Agreement](CLA.md). infraharvest is AGPL-3.0-only, with a commercial offering planned alongside it, and that only works if every contribution can be relicensed, which is the right the CLA grants. A DCO sign-off isn't enough: it says you had the right to submit the change, but grants no relicensing right.

The `cla` workflow asks on your first pull request: comment

> I have read the CLA Document and I hereby sign the CLA

once, and it covers all your later pull requests.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```text
fix(aws): paginate ListQueues
feat(selection): add tag filters
ci: pin actions to commit SHAs
refactor!: keep only the AWS, Azure and Google Cloud providers
```

Types: `feat`, `fix`, `refactor`, `perf`, `test`, `docs`, `style`, `ci`, `build`, `chore`, `revert`. The scope is the provider or the package (`aws`, `cli`, `engine`, `catalogue`). A breaking change takes `!` and a `BREAKING CHANGE:` footer saying what users must change.

## Setting up

You need Git, Go (the version in `go.mod`), and the [GitHub CLI](https://cli.github.com/) for the checks that run on GitHub. On Windows, use Git Bash for the scripts and `make`.

The module is large: compiling and testing all of it needs several GB of RAM. You don't have to: the checks below that compile run on GitHub on demand, and everything else runs locally in seconds.

### Tools for the fast checks

Each is optional. The pre-commit hook skips a check whose tool is missing, says how to install it, and CI runs the check anyway.

| Tool | What it checks | Install |
|---|---|---|
| gofmt | Go formatting | Comes with Go. |
| goimports | Import grouping | `go install golang.org/x/tools/cmd/goimports@latest` (builds goimports only) |
| [shellcheck](https://github.com/koalaman/shellcheck#installing) | Shell scripts | `brew install shellcheck`, `sudo apt install shellcheck`, `winget install --id koalaman.shellcheck` or `scoop install shellcheck` |
| [actionlint](https://github.com/rhysd/actionlint/blob/main/docs/install.md) | GitHub workflows | `brew install actionlint`, `winget install actionlint` or `scoop install actionlint` |
| [Node.js](https://nodejs.org/) | Markdown, through `npx markdownlint-cli2` | From nodejs.org or your package manager. |
| GNU make | The `make` targets | Included on Linux and macOS; on Windows `scoop install make` or `choco install make`, or run the scripts directly. |

`make tools` shows which are installed.

### Git hooks

Turn the hooks on once per clone:

```sh
make hooks
# or, without make:
git config core.hooksPath scripts/hooks
```

Before each commit, `scripts/hooks/pre-commit` runs the fast checks on the staged files, as they are staged: gofmt, goimports, the licence header of Go files, the names no file may use (`scripts/check-names.sh`), shellcheck, actionlint and markdownlint. `scripts/hooks/commit-msg` checks the message's first line. They need no Python and no build of the module.

To run the same checks on every file: `make fast`, or one of them, such as `make fast-markdownlint`. `make fmt` formats the Go files you have changed.

## Checking a change

CI runs everything on each pull request: `go mod tidy -diff`, a build and `go test ./...` on Linux, the OS-sensitive packages on Windows, the whole module on macOS, builds for linux/arm64 and windows/arm64, the race detector, a short fuzzing run of the parsers and secret detectors (`Fuzz*` tests), `govulncheck`, end-to-end imports against the AWS, Azure and Google Cloud emulators ([e2e/README.md](e2e/README.md)), the licence headers, names, `reuse lint` and the single root commit of the history, and [MegaLinter](https://megalinter.io) (`.mega-linter.yml`), which runs every linter: golangci-lint on the whole module (any finding fails), workflows, scripts, Markdown, YAML, JSON, the Dockerfile, Terraform, secrets and vulnerable dependencies. MegaLinter reports in one comment on the pull request. The same runs after each merge to `main`. A pull request that changes only documentation, linter configuration or workflows other than `.github/workflows/test.yml` skips the Go and end-to-end jobs; the `test`, `e2e` and `checks` jobs, which gather them, still report, and pass when each passed or was skipped.

To check a branch before opening a pull request, push it, then run the checks on GitHub:

```sh
make ci-lint                                   # MegaLinter, golangci-lint included, on the whole repository
make ci-check PKG=./internal/engine/...        # go vet and go test, on ubuntu-latest
make ci-check PKG=./internal/fsutil/... RUNNER=windows-latest
make ci-watch RUN=<run-id>                     # follow a run until it ends
make ci-bench BENCH=ScanSecrets BASE=<sha>     # benchmarks with CPU profiles, compared with a commit
```

Without `make`, the same with `gh`:

```sh
gh workflow run megalinter --ref <branch>
gh workflow run remote --ref <branch> -f task=check -f packages="./internal/harvest/... ./internal/providers/aws/..." -f os=ubuntu-latest
gh run watch
```

If your machine can take it, the same checks run locally: `make lint` (golangci-lint with the repository's configuration, at the version CI uses), `make vet` and `make test`, each with `PKG=` and `TAGS=`, and MegaLinter as CI runs it, with Docker: `npx mega-linter-runner --flavor go -e GOTOOLCHAIN=auto`. `TAGS=minimal,aws` compiles only the AWS provider, which is much lighter:

```sh
make test PKG=./internal/cli/... TAGS=minimal,aws
```

### Golden files

Golden tests compare output with files under `testdata/` (see [docs/catalogue.md](docs/catalogue.md#golden-tests)). The reference pages are golden files too: `docs/reference/cli.md` is generated from the commands and their flags, and `docs/reference/services.md` from each provider's services, their lister code and the catalogue. After a change that alters them, rewrite them on GitHub, download the result into your working tree, and review the diff before committing it:

```sh
make ci-golden PKG=./internal/catalogue        # or ./internal/cli for docs/reference/
make ci-download RUN=<run-id>
git diff
```

Pushing a branch named `golden/<anything>` rewrites the catalogue's golden files the same way. Never edit a golden file by hand.

### Dependencies

After changing imports or `go.mod`, tidy on GitHub and commit what it returns:

```sh
make ci-tidy
make ci-download RUN=<run-id> NAME=go-mod
```

Pushing the branch as `tidy/<anything>` does the same. Pin GitHub Actions to a full commit SHA, with the version in a trailing comment. Dependabot proposes updates weekly.

### Sandbox

The `sandbox-aws`, `sandbox-azure` and `sandbox-google` workflows import a real estate from the AWS sandbox account, the Azure sandbox subscription and the Google Cloud sandbox project. They run by hand, after a reviewer approves them, and only for changes to their cloud's listers or permissions template, the catalogue, the engine, the adapters or the sandbox itself. Say in the pull request whether any should run, and why.

## Documentation

The documentation is the Markdown under `docs/`, which GitHub renders and [MkDocs Material](https://squidfunk.github.io/mkdocs-material/) builds into the site (`mkdocs.yml`, with the navigation). The `docs` workflow builds it strictly on every change, so a broken link or a page missing from the navigation fails, and publishes it from `main` to GitHub Pages. Its `docs` job reports on every pull request, and passes when the build passed or, for a pull request that doesn't change the site, was skipped. The Markdown rules are in `.markdownlint-cli2.yaml` (see [docs/development.md](docs/development.md#markdown)). To preview the site: `pip install -r docs/requirements.txt && mkdocs serve`.

The pages under `docs/reference/` are generated: change the code they come from and rewrite them as [Golden files](#golden-files) describes.

## Profiling

To see where a run spends time or memory, or which goroutines it leaks, name files for the profiles:

```sh
INFRAHARVEST_CPU_PROFILE=cpu.pprof INFRAHARVEST_MEM_PROFILE=mem.pprof INFRAHARVEST_GOROUTINELEAK_PROFILE=leak.pprof infraharvest import aws ...
go tool pprof -top cpu.pprof
go tool pprof -sample_index=inuse_space -top mem.pprof
go tool pprof -traces leak.pprof
```

The CPU profile covers the whole run; the heap profile is taken at its end, and so is the goroutine leak profile, which lists the goroutines blocked on a channel or lock that nothing else can reach any more. Terraform and the providers run as their own processes, so they aren't in the profiles.

## Licence

By contributing, you agree that your contributions are licensed under the repository's licence (see [LICENSE](LICENSE)). The [CLA](CLA.md) you sign also grants the project owner the right to relicense them, under another open-source or a commercial licence (its section 3): the two go together, as [Contributor License Agreement](#contributor-license-agreement) explains.

Every Go file starts with a two-line licence header, which the pre-commit hook and CI check (`scripts/check-headers.sh`):

<!-- REUSE-IgnoreStart -->
```go
// Copyright 2026 <your name>
// SPDX-License-Identifier: AGPL-3.0-only
```
<!-- REUSE-IgnoreEnd -->
