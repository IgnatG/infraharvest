# Development

How infraharvest's code is written: where things go, the conventions the code already follows, how it is tested, and how a change gets from a branch into `main`. [CONTRIBUTING.md](https://github.com/IgnatG/infraharvest/blob/main/CONTRIBUTING.md) covers setting up the tools; this page is the reference behind it. Where a rule has an example in the code, the path is given: read it before writing something similar.

The module is large: three cloud SDKs, so compiling all of it takes several GB of RAM and minutes. Nothing here assumes you can. The fast checks never build the module, and the heavy ones (lint, vet, tests, golden files, `go mod tidy`) run on GitHub on demand (see [Checks](#checks)).

## Layout

Everything but `main.go` is under `internal/`: infraharvest has no Go API for others to import. `main.go` stays at the root so that `go install github.com/IgnatG/infraharvest@latest` keeps working, and only sets up logging and turns the outcome into the exit code.

| Package | What belongs there |
|---|---|
| `internal/cli` | The Cobra commands and nothing else: flags, help text, wiring to `internal/importer`. |
| `internal/cli/providers` | One file per provider command (`provider_cmd_<provider>.go`) with its own flags, the shared flags, and the registry of what is compiled in. |
| `internal/importer` | The orchestration of an import, with no Cobra in it: `Options`, `Import`, `ImportEach`, `Run`, `Verify`, the report and the exit codes. Anything a command does that can be tested without a command goes here. |
| `internal/harvest` | The API listers implement and the importer drives: `Provider`, `Lister`, `ListerBase`, `Resource`, `Registry`, filters, tags and labels. |
| `internal/providers/aws`, `azure`, `google` | The listers: the only code that calls the clouds' APIs. |
| `internal/catalogue` | What infraharvest knows about each Terraform resource type, as YAML under `data/`, its schema and its golden tests (see [Catalogue](catalogue.md)). |
| `internal/engine` | Runs Terraform or OpenTofu: import blocks, `plan -generate-config-out`, repairs, references, locals, modules, secrets, and the verification gate. It never writes state and never applies. |
| `internal/adapters` | Curated module adapters (module, exact version, mapping), the snapshots of module interfaces they are checked against, and `cmd/adaptercheck`, which the nightly `adapters` workflow runs. |
| `internal/discovery` | Runs the listing: one discoverer per service and scope (account and region, subscription, project), each with its deadline, a bounded number at a time. |
| `internal/selection`, `report`, `managed`, `config` | The selection file and its default rules, the report, the resources state already manages (S3, GCS, Azure Blob, HCP Terraform), and the configuration file. |
| `internal/mcpserver`, `picker` | The MCP server for AI agents and the terminal picker. |
| `internal/ratelimit`, `fsutil`, `version` | Small shared helpers: request rates per cloud API, atomic file writes, and the version a build reports (set at link time by release builds). |
| `e2e/` | Round trips against cloud emulators, behind the `e2e` build tag ([e2e/README.md](https://github.com/IgnatG/infraharvest/blob/main/e2e/README.md)). |
| `permissions/`, `action/`, `docs/` | Read-only IAM templates, the GitHub Action, and this site. |

Some rules about where code goes:

- Knowledge about a resource type is data in the catalogue, not code in a lister: the ID Terraform imports it by, resources the default selection skips, child resources, data sources, arguments to omit. What needs the cloud to decide, such as whether a VPC is the default one, stays in the provider's code.
- A lister lists. It records each resource's ID, name, Terraform type and the attributes filters and catalogue templates need; it doesn't build import IDs or decide what to skip.
- Cobra stays in `internal/cli`. A command reads its flags into `importer.Options` and calls the importer, so the behaviour is tested without a command line (`internal/importer/*_test.go`).
- A helper used by one package stays in that package. A new package under `internal/` is for something with its own job and its own tests, like `internal/fsutil`.

## Licence headers

infraharvest is licensed under AGPL-3.0-only ([LICENSE](https://github.com/IgnatG/infraharvest/blob/main/LICENSE)).

Every Go file starts with this header, then a blank line, then any build constraint, then the package's doc comment (`internal/cli/providers/provider_cmd_aws.go`):

<!-- REUSE-IgnoreStart -->
```go
// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || aws

package providers
```
<!-- REUSE-IgnoreEnd -->

Contributors put their own name and the year the file was created. `scripts/check-headers.sh` fails when a tracked Go file lacks the `SPDX-License-Identifier` line: the `repository` job of the `tests` workflow runs it on every pull request, and the pre-commit hook on the staged files, where it also checks that a new file starts with both lines. `make fast-headers` runs it on every file.

New shell scripts, workflows, YAML files and Makefiles carry the same two lines as comments, after the `#!` line in a script (`scripts/fast-checks.sh`, `Makefile`). Some existing ones have none, such as most workflows, `install.sh`, `.goreleaser.yaml` and the `Dockerfile`; add it when you next change one. Markdown, JSON and data files carry none. A file without a header is covered by `REUSE.toml`, which gives it the same copyright and licence, and the `repository` job of the `tests` workflow (`.github/workflows/test.yml`) runs [`reuse lint`](https://reuse.software/) on every pull request. Text that quotes a licence line without being one, such as the header examples on this page, goes between `REUSE-IgnoreStart` and `REUSE-IgnoreEnd` comments.

## Names

`scripts/check-names.sh` fails when a tracked file uses one of a few names infraharvest doesn't use (an old project name, label prefix and package name; the script lists them). It runs where the header check runs, and `make fast-names` runs it on every file. A file that really needs one of them goes into `.github/names-allowlist.txt`, with the reason; the list is empty.

## Rewriting code

To change how something works without changing what it does, record the behaviour first, in its own pull request: golden tests of every exported function over table-driven inputs, and of every interface's method set and signature (`internal/harvest/golden_characterization_test.go`), or of the external contract, such as every command's flags (`internal/cli/contract_golden_test.go`). The `golden` task of the `remote` workflow records them from the current code. The rewrite then passes against those goldens unchanged; any difference they allow is deliberate and named in the pull request.

## Comments

The code is read more than written, so comments carry the reasoning. Write them as plain, complete sentences that say what something is or does, and why when it isn't obvious.

- Every package has a doc comment saying what it is for, and what it never does where that matters (`internal/engine/engine.go`: "It never writes state and never applies.").
- Every exported identifier, and any unexported one whose purpose isn't plain from its name, has a doc comment starting with its name. Say what callers need to know, not how it works: `fsutil.WriteFile` explains that an interrupted import leaves the previous file whole.
- Document struct fields one by one, and group long option structs with comments, as `importer.Options` and `engine.Options` do.
- Inline comments say why: `// SetEnv replaces the inherited environment and rejects variables that tfexec manages itself (TF_LOG, TF_CLI_ARGS, ...), so drop those.` A comment that restates the next line goes.
- In tests, say what the case guards against: `// Before the fix, the ignored error left the response nil and this panicked.`
- Prose is in British English, as the documentation is: catalogue, licence, behaviour.

## Errors

- **Return errors; never mistake a failure for an empty result.** A lister that can't list returns the error, so a run with missing resources fails or is reported as partial instead of silently generating less (`internal/providers/aws/guarded_lister.go`, `List`). The exceptions are deliberate and logged: a service AWS doesn't offer in the region, or one Resource Explorer shows is empty.
- **Wrap with what was being done**: `fmt.Errorf("terraform init: %w", err)`, `fmt.Errorf("import into %s: %w", dir, err)`. Messages are lower case, with no trailing full stop and no "failed to" or "error:": the wrapping already says it failed (`revive` enforces the case).
- **Say what to do** where the user can act: `errors.New("the picker needs a terminal: edit the selection file instead")` (`internal/importer/pick.go`).
- **Report everything at once** where there can be several problems, with `errors.Join`: the catalogue's checks (`internal/catalogue/check.go`), Terraform's diagnostics (`internal/engine/diagnostics.go`).
- **Match errors by type, not by text**: `errors.As` with `smithy.APIError`, `*net.DNSError` (`unavailableInRegion` in `guarded_lister.go`). Match on a message only where the API gives nothing else, and say so in a comment.
- **Exit codes** come from the error: commands return an `*importer.ExitError` with one of the `report.Exit*` codes, and `main` maps it through `importer.ExitCode`.
- Library code doesn't panic. `Must*` functions are for data built into the binary and checked by tests (`catalogue.MustBuiltin`).
- An error deliberately ignored on a clean-up path is assigned to `_` (`internal/fsutil/fsutil.go`).

## Context

- Anything that does I/O or may run long takes a `context.Context` as its first argument: `importer.Import(ctx, ...)`, every method of `engine.Terraform`.
- The context starts at the command (`c.Context()` in `internal/cli/providers/command.go`) and is passed down, so an interrupt stops the whole import; the sandbox's resume test relies on it.
- A lister makes every API call with `g.Context()` (`harvest.ListerBase.Context`): it is set for each service, carries the `--list-timeout` deadline, and is cancelled when the user interrupts. Code below the command never calls `context.Background()` or `context.TODO()`, except `harvest.ListerBase.Context` for a lister no context was set for. A provider's `Init` takes the import's context for what it checks before listing, such as a Google region or an Azure Stack cloud's endpoints. A client that outlives the call it is made for takes `context.WithoutCancel(ctx)`, which keeps the context's values without its cancellation (`limitedClient` in `internal/providers/google/client.go`).
- Whoever owns a timeout sets it, where it applies (`internal/adapters/cmd/adaptercheck/main.go`).

## Logging and output

- **Standard output carries results only**: the service list, `--format=json`'s report (`report.WriteJSON(os.Stdout)` in `internal/importer/engine_run.go`). The MCP server runs the binary and parses its standard output as data (`internal/mcpserver`), so a stray line there breaks it.
- **Everything else is a log line**, through the standard `log` package. `main` sends it to standard error, through a writer that drops the `[TRACE]` and `[DEBUG]` lines of client libraries, which can contain whole HTTP requests (`internal/logging`). Never `fmt.Print` progress.
- Start a provider's messages with its name and say what happened and what infraharvest does about it: `aws: sqs isn't available in this region; skipping it (...)`.
- Detail that helps only when debugging goes behind the lister's `Verbose()` (`--verbose`).
- Never log credentials, secret values or whole API responses.

## Listing resources

A provider is a `harvest.Provider` whose `Services` maps each service name, as users type it in `--services`, to a lister. A lister embeds the provider's base type (`aws.Service`, `azure.Service`; Google's embed `harvest.ListerBase`) and implements `List`, which lists the service's resources and records each with `harvest.Record` or `harvest.RecordBare`: the ID, a name for the label, the Terraform type and the provider.

- Listers only read. They call list, describe and get APIs, and nothing that changes the cloud or reads data: the read-only role denies it (`permissions/aws/deny-data-reads.json`), so a lister that needs it fails for every user who follows [Permissions](permissions.md). When a lister needs access the templates under `permissions/` don't grant, change them in the same pull request.
- Record the attributes the catalogue's import ID templates and skip rules use, and the ones `--tag` and `--where` should match.
- Leave out what the cloud owns or can't be imported through the catalogue (`skip`, `not_importable`), not with `if` statements in the lister.
- Types without a dedicated lister can come from the Cloud Control API with a `cloud_control` catalogue entry (`internal/providers/aws/cloudcontrol.go`).

### Pagination

Every list call pages until the API says it is done, and a page that doesn't advance stops the listing instead of looping:

- With an AWS SDK paginator, pass `stopOnDuplicateToken` as its option, and page with the service's context (`internal/providers/aws/stepfunctions.go`):

    ```go
    p := sfn.NewListStateMachinesPaginator(svc, &sfn.ListStateMachinesInput{}, stopOnDuplicateToken)
    for p.HasMorePages() {
        page, err := p.NextPage(g.Context())
        if err != nil {
            return err
        }
        // record page.StateMachines
    }
    ```

- An AWS API with a `NextToken` or `NextMarker` but no SDK paginator goes through `paginateByMarker`, which fails if a marker comes back twice (`internal/providers/aws/apigatewayv2.go`, `waf_classic.go`).
- Azure and Google listers use their SDK's pager or iterator with the service's context.
- Don't write a pagination loop by hand. Both helpers live in `internal/providers/aws/pagination.go` and are tested in `pagination_test.go`.

## Build tags

- Provider code that the command tree compiles in carries `//go:build !minimal || <provider>`, and registers itself from `init()` (`internal/cli/providers/provider_cmd_aws.go`). Its tests carry the same constraint (`aws_accounts_test.go`).
- `-tags minimal` builds no provider; `-tags minimal,aws` builds AWS only. The `build-tags` job of the `tests` workflow checks both. Locally, `make test PKG=./internal/cli/... TAGS=minimal,aws` compiles one SDK instead of three.
- Tests that need more than the Go toolchain have their own tag: `integration` (the engine against real Terraform), `e2e` (emulators, combined with a provider: `e2e && (!minimal || azure)`), and `sandbox` (a real AWS account, run only by hand).

## Tests

A bug fix comes with a test that fails without it. New behaviour comes with tests of it.

- **Table-driven**, with the inputs and expectations side by side and a failure message that names the call: `t.Errorf("Label(%q): got %q, want %q", name, got, want)` (`internal/engine/names_test.go`).
- **Fakes, not mocks.** Depend on the small interface you use and fake it: `engine.Terraform` is the part of `tfexec` the engine calls, faked by `fakeTerraform` (`internal/engine/engine_test.go`); `mcpserver.Runner` by `fakeRunner`.
- **AWS listers run against a fake API**: `useFakeAPI(t, handler)` (`internal/providers/aws/fakeapi_test.go`) sends the SDK's requests to a function that switches on the operation and returns the response body, including error bodies shaped like AWS's. Test the error paths, not only the happy one (`TestWafv2ReportsOtherAssociationErrors`). Azure and Google listers fake the transport or the pager the same way (`internal/providers/azure/network_listers_test.go`, `internal/providers/google/cloud_asset_test.go`).
- Helpers call `t.Helper()`; tests restore what they change with `t.Cleanup`, and write files under `t.TempDir()`.
- **Golden files** (`testdata/*.golden.*`) record output that is long or easier to review as a file: the catalogue's import blocks, the harvest core's behaviour, the command-line contract, and the generated references (`docs/reference/cli.md`, `docs/reference/services.md`). Tests take `-update` to rewrite them. Don't edit a golden file by hand: run the `golden` task of the `remote` workflow (`make ci-golden PKG=<package>`), download what it rewrote (`make ci-download RUN=<id>`), and review the diff before committing it.
- **Characterization before a rewrite**: record what the code does today, in its own pull request, before changing how it does it (see [Rewriting code](#rewriting-code)).
- The emulator round trips in `e2e/` must keep planning with no changes on both Terraform and OpenTofu. To cover a new service, add it to the emulator's fixture as [e2e/README.md](https://github.com/IgnatG/infraharvest/blob/main/e2e/README.md) describes.

## Formatting and lint

- `gofmt -s`, and imports in three groups: the standard library, third-party modules, then this module's packages (`internal/engine/engine.go`). The pre-commit hook checks the grouping with `goimports -local github.com/IgnatG/infraharvest`. CI doesn't check the third group yet: `.golangci.json` gives the `goimports` formatter no `local-prefixes`.
- golangci-lint runs with `.golangci.json` (`gocritic`, `revive`, `unconvert`, `unparam`, and the `gofmt` and `goimports` formatters) on the whole repository for every pull request, in MegaLinter. Any finding fails the check, wherever it is, not only on changed lines.
- [MegaLinter](https://megalinter.io) runs every linter CI runs, in the `megalinter` workflow, and reports in one comment on the pull request and in the run's summary. `.mega-linter.yml` lists the linters and says why the others are off: golangci-lint, actionlint and zizmor (workflows), shellcheck, markdownlint, lychee (links), hadolint, editorconfig-checker, yamllint, v8r (schemas), jsonlint, checkov, osv-scanner, betterleaks and trufflehog. `make ci-lint` runs it on a branch; `npx mega-linter-runner --flavor go -e GOTOOLCHAIN=auto` runs it locally, with Docker.
- Fix findings rather than silencing them. New code has no `//nolint`; the few there are mark legacy types due to be rewritten.

## Dependencies

- Every new module adds to a build that is already heavy: prefer the standard library and the SDKs already in `go.mod`, and say in the pull request why a new one is worth it.
- After changing imports or `go.mod`, run `go mod tidy` on GitHub: `make ci-tidy`, or push a branch named `tidy/<anything>`, then `make ci-download RUN=<id> NAME=go-mod` and commit `go.mod` and `go.sum`. The `tests` workflow fails when they aren't tidy.
- GitHub Actions are pinned to a full commit SHA with the version in a trailing comment, and tools downloaded in workflows to a version and a SHA-256 digest. Dependabot proposes updates weekly.
- `govulncheck` runs on the built binary; a finding fails unless `.github/govulncheck-allowlist.txt` covers it, with the reason.

## Markdown

The documentation is Markdown under `docs/`, built by MkDocs Material (`mkdocs.yml`). `.markdownlint.yaml` holds the rules, which MegaLinter and the pre-commit hook both read:

- One paragraph or list item per line; no hard wrapping.
- `-` for bullets, nested by four spaces, as Python-Markdown needs.
- Every fenced code block names its language (`sh`, `go`, `yaml`, `text`).
- Headings, not bold lines, for sections.
- Tables are written compactly, with `|---|` rules.

A new page goes into `mkdocs.yml`'s navigation; the `docs` workflow builds strictly, so an unlisted page or a broken link fails. Link to files outside `docs/` with their address on GitHub, as MkDocs can't resolve relative links that leave the site.

## Branches, commits and pull requests

- Branch from `main` and open pull requests against it. The site is published from `main`.
- Name branches after the change's type: `feat/`, `fix/`, `refactor/`, `test/`, `ci/`, `docs/`, `build/`, `chore/`. Branches named `golden/<anything>` and `tidy/<anything>` start those tasks of the `remote` workflow when pushed.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/): `type(scope): summary`, lower case, with no trailing full stop. The scope is the provider or package (`aws`, `cli`, `harvest`, `engine`, `catalogue`, `sandbox`). A breaking change takes `!` and a `BREAKING CHANGE:` footer. The `commit-msg` hook checks the first line.
- Pull requests are squash-merged, so the title becomes the commit on `main`: write it as a Conventional Commit too. One logical change per pull request.
- The description says what changes and why, how it was tested, and ends with whether the AWS sandbox ran and why: it is run by hand (`sandbox-aws` workflow), only for changes to the AWS listers, the catalogue, the engine, the adapters or the sandbox itself.
- A pull request merges once every check is green.

## Releases

The `release` workflow publishes a release from `main`: it runs the tests, tags the commit, and has GoReleaser (`.goreleaser.yaml`) build the archives, the signed checksum file, the SBOMs, the provenance and the signed container image. The release notes are generated: GitHub lists the pull requests merged since the previous release, so their titles (the squashed Conventional Commits) are the changelog, and GoReleaser adds how to install and verify the release below them.

1. Run `gh workflow run release --ref main -f version=v0.2.0`. A version with a suffix, such as `v0.2.0-rc.1`, is published as a prerelease, which GitHub never counts as the latest: the install script and the image's `latest` tag skip it.
2. After the release, open a pull request that sets `devVersion` in `internal/version/version.go` to the next version with `-dev` (`v0.3.0-dev`), so builds from `main` don't report the version just released or one before it.

## Checks

| Check | Before committing | On GitHub |
|---|---|---|
| `gofmt -s`, import grouping | pre-commit hook, `make fast` | `megalinter` (golangci-lint's formatters) |
| Licence headers, names, `reuse lint`, a single root commit | pre-commit hook, `make fast` (not `reuse lint` or the root commit) | `tests` (`repository`) |
| Shell scripts, workflows | pre-commit hook (shellcheck, actionlint) | `megalinter` (shellcheck, actionlint, zizmor) |
| Markdown | pre-commit hook (markdownlint-cli2) | `megalinter` (markdownlint, lychee), `docs` (strict MkDocs build) |
| YAML, JSON, Dockerfile, Terraform, secrets, vulnerable dependencies | none | `megalinter` |
| Commit message | `commit-msg` hook | the pull request title, at review |
| golangci-lint | `make lint` (compiles) | `megalinter` on every pull request; `make ci-lint` on a branch |
| `go vet`, `go test` | `make vet`, `make test` (compile) | `tests` on every pull request; `make ci-check PKG=...` on a branch |
| Golden files | `make golden` (compiles) | `make ci-golden PKG=...` |
| `go mod tidy` | none | `make ci-tidy`; `tests` checks it |
| Emulator round trips, race detector, Windows | none | `tests` on every pull request |
| macOS | none | `macos` after merges to `main`, weekly, and by hand |

The fast checks need only the tools they run (gofmt, goimports, shellcheck, actionlint, Node.js), each optional: the hook skips a check whose tool is missing and says how to install it, and CI runs it anyway. Run the compiling checks locally only if your machine can take it; otherwise push the branch and use the `ci-*` targets, which need the [GitHub CLI](https://cli.github.com/). `make help` lists every target.
