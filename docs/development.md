# Development

How infraharvest's code is written: where things go, the conventions the code already follows, how it is tested, and how a change gets from a branch into `main`. [CONTRIBUTING.md](https://github.com/IgnatG/infraharvest/blob/main/CONTRIBUTING.md) covers setting up the tools; this page is the reference behind it. Where a rule has an example in the code, the path is given: read it before writing something similar.

The module is large: three cloud SDKs, so compiling all of it takes several GB of RAM and minutes. Nothing here assumes you can. The fast checks never build the module, and the heavy ones (lint, vet, tests, golden files, `go mod tidy`) run on GitHub on demand (see [Checks](#checks)).

## Layout

Everything but `main.go` and `pkg/cfn` is under `internal/`: infraharvest has no Go API for others to import, but the CloudFormation translator, which can be used on its own. `main.go` stays at the root so that `go install github.com/IgnatG/infraharvest@latest` keeps working, and only sets up logging and turns the outcome into the exit code.

| Package | What belongs there |
|---|---|
| `internal/cli` | The Cobra commands and nothing else: flags, help text, wiring to `internal/importer`, and `CheckRun`, which checks a provider command's run as its `--check` does, for the MCP server. |
| `internal/cli/providers` | One file per provider command (`provider_cmd_<provider>.go`) with its own flags, the shared flags, and the registry of what is compiled in. Each describes its command with a `definition` (`command.go`): the provider's flags, the checker of what they say, and the planner that turns them into the scopes and calls to import. |
| `internal/importer` | The orchestration of an import, with no Cobra in it: `Options`, `CheckOptions`, `Run` with its `ImportEach` and `MergeEnvironments`, `ImportCalls`, `Verify`, `Drift`, the report and the exit codes. Anything a command does that can be tested without a command goes here. |
| `internal/harvest` | The API listers implement and the importer drives: `Provider` with its `Profile`, the optional capabilities (`capabilities.go`), `Lister`, `ListerBase`, `Resource`, `Registry`, filters, tags and labels. |
| `internal/providers/aws`, `azure`, `google` | The listers: the only code that calls the clouds' APIs. |
| `internal/catalogue` | What infraharvest knows about each Terraform resource type, as YAML under `data/`, its schema and its golden tests (see [Catalogue](catalogue.md)), and in `schemameta`, the defaults and argument constraints read out of the providers' code, with their generator (see [Schema metadata](catalogue.md#schema-metadata)). |
| `internal/engine` | Runs Terraform or OpenTofu: import blocks, `plan -generate-config-out`, repairs, references, locals, modules, secrets, and the verification gate, for a root of its own (`Generate`) or added to one (`Add`), and the root environments share (`MergeRoots`). It never writes state and never applies. |
| `internal/adapters` | Curated module adapters (module, exact version, mapping), the snapshots of module interfaces they are checked against, and `cmd/adaptercheck`, which the nightly `adapters` workflow runs. |
| `internal/discovery` | Runs the listing: one discoverer per service and scope (account and region, subscription, project), each with its deadline, a bounded number at a time. |
| `internal/driftpolicy` | What `infraharvest drift` does with each change it finds: the rules of `--accept`, `--keep` and `--rules`, the decisions file, the order they decide in (`Decider`), and the digest of the roots (`RootsDigest`) that the MCP server confirms a run with and drift checks again. The importer applies what it decides. |
| `internal/selection`, `report`, `managed`, `config` | The selection file and the default selection (`Sources`, which decides each listed resource and why), the report, the resources state already manages, and the configuration file. `managed` links no cloud SDK: the stores it reads state from are packages of their own under `managed/stores/` (`s3`, `gcs`, `azblob`, `hcp`), each set by its provider's definition in `internal/cli/providers`, so a single-cloud build links that cloud's store only. |
| `pkg/cfn`, `pkg/cfn/hclname`, `internal/stackintent` | The CloudFormation translator (`infraharvest cfn convert`), the names and numbers it writes that the rest must write alike, and the stack intent: a stack's parameters, outputs and conditions carried into the root generated from its live resources (AWS `--cfn-stack`). |
| `internal/mcpserver`, `picker` | The MCP server for AI agents and the terminal picker. |
| `internal/ratelimit`, `fsutil`, `filelock`, `logging`, `version`, `tftypes` | Small shared helpers: request rates per cloud API, atomic file writes, locks on the output directory and the provider cache, the filter that drops client libraries' debug lines, the version a build reports (set at link time by release builds), and the resource types that are other names of each other, such as `aws_alb` and `aws_lb`, which the engine and `managed` match alike. |
| `internal/repocheck` | Tests of the repository's scripts and `.gitignore`, run against real Git repositories. |
| `e2e/` | Round trips against cloud emulators, behind the `e2e` build tag ([e2e/README.md](https://github.com/IgnatG/infraharvest/blob/main/e2e/README.md)). |
| `permissions/`, `action/`, `docs/` | Read-only IAM templates, the GitHub Action, and this site. |

Some rules about where code goes:

- Knowledge about a resource type is data in the catalogue, not code in a lister: the ID Terraform imports it by, resources the default selection skips, child resources, data sources, arguments to omit. What needs the cloud to decide, such as whether a VPC is the default one, stays in the provider's code.
- A lister lists. It records each resource's ID, name, Terraform type and the attributes filters and catalogue templates need; it doesn't build import IDs or decide what to skip.
- Cobra stays in `internal/cli`. A command reads its flags into `importer.Options` and calls the importer, so the behaviour is tested without a command line (`internal/importer/*_test.go`).
- A helper used by one package stays in that package. A new package under `internal/` is for something with its own job and its own tests, like `internal/fsutil`.
- What sets a provider apart goes in its `Profile` (`internal/harvest/profile.go`), not in code that tells providers apart by name: its command's name; what one import covers (an account, subscription or project), what its IDs are like and how the MCP server describes it for the user to confirm; its family of curated modules; the services `*` leaves out and those that list after the others; its registry source, the tag a block label is made from, how it applies tags, which file of a root a resource goes in, and what a root that environments share reads of its account and region. The configuration file, the MCP server and the help read the profiles of the providers compiled in from `internal/cli/providers` (`Profiles`); `config.Load` takes them from its caller, as the configuration can't import the providers. A behaviour only some providers have, where the provider runs code for it, is an optional interface in `capabilities.go`, such as `ProviderWithScope`. A new provider still adds a section to an environment of the configuration file (`config.Environment`) and its fields to the MCP tools' `Scope`, as both are formats others read.

### Inside an import

An import goes through a few modules, each owning one decision. Change a decision in its module, not where it is used:

- **Checks.** A provider command takes flags only (`cobra.NoArgs`). Before it plans anything, `importer.CheckOptions` rejects what no call could do, then the provider's own checker what its flags say (`definition.flags` in `internal/cli/providers/command.go`). `--check` stops there and exits, after the configuration file has applied (`withEngineRun` in `internal/cli/run.go`); `config.File.Apply` refuses `check` and `config` as settings. The MCP server checks an import the same way, through `cli.CheckRun`, before it asks the user to confirm it.
- **Listing and selection.** `ImportCalls` lists every call of a scope (`listCall` in `internal/importer/import_stages.go`) before any generates, so that each root refers to the others. `listCall` reads as its stages: it prepares the call (provider, catalogue, services and scope, into a `callContext` beside the call's `Options`, which stay as the command gave them), lists and selects, lays out the roots, gets the toolchain (binary, root files and module adapters), adds the children, and places each resource in its root with its whole import block; `engineOptions` builds the engine's options in one place, from the catalogue's part (`catalogueEngineOptions` in `internal/importer/catalogue.go`, which maps the catalogue's data into engine options so that the catalogue never imports the engine) and the stages'. `selection.Sources` (`internal/selection/sources.go`) holds why the default selection leaves each listed resource out (the catalogue's skip rules, the provider's defaults, the tags, `--managed-state`, the import scope) and decides between them in one place, so that a resource is left out for the same reason whichever way it is asked about: `discover` writes each resource's `Reason` into the selection file, and `import` asks `Decide`, which weighs a selection file's entries and rules against it (`internal/importer/selection.go`).
- **The root's lifecycle.** `decideRoot` (`internal/importer/rootlife.go`) decides once, from the run's mode and what earlier imports left on disk, whether a root is generated afresh or again, resumed from its checkpoint, added to, or refused with a reason; `applyRoot` then does it. The checkpoints in `<out>/.infraharvest` are read and written there too.
- **The call's tools.** What a call reaches outside the process through goes through `callTools` (`internal/importer/call_tools.go`): the engine binary, the registry the root files pin the provider from, and every write the engine makes into a root: generating, adding to, vacating and merging roots, and their READMEs and moved blocks. Tests replace a field to fake that step; a nil field uses the real one.
- **Generate and add.** `engine.Generate` and `engine.Add` run one pipeline; a `policy` (`internal/engine/engine.go`) is what tells them apart, such as the names an add leaves alone and whether it makes local modules. When an edit makes the pipeline plan `generated.tf` again, the log says which (`replanReason`).
- **The merged root's layout.** Where a root that environments share keeps what differs between them (`vars/common.tfvars`, and `terraform.tfvars`, `imports.tfvars` and `backend.hcl` under `vars/<env>/`, the `deploy_<feature>` flags, the workspaces' state) is decided in `internal/engine/mergedlayout.go` alone: whatever writes, plans or reads such a root back takes its paths from there.

### Dependency directions

Imports run one way across the modules' seams, and CI fails a change that turns one round. golangci-lint's `depguard` holds most of them, each rule in `.golangci.json` with the reason a finding prints; `go test ./pkg/cfn` holds the last:

| Package | Doesn't import | Why |
|---|---|---|
| `internal/engine` | `internal/providers/*`, `harvest`, `importer`, `cli`, `mcpserver` | It runs Terraform on import blocks for any provider; the importer drives it. |
| `internal/harvest` | `report`, `engine`, `importer`, `internal/providers/*`, `cli`, `mcpserver` | It is the API listers implement: it lists and never runs Terraform. |
| `internal/providers/*` | `cli`, `mcpserver`, each other, and `importer` outside tests | Listers implement `harvest`; `internal/cli/providers` registers them, and each builds alone (`-tags minimal,aws`). |
| `internal/mcpserver` | `internal/providers/*`, `importer` | It reads the providers compiled in through `internal/cli/providers`, and runs the `infraharvest` binary for imports and drift, whose rules and decisions it reads through `internal/driftpolicy`. |
| `internal/driftpolicy` | `importer`, `cli`, `mcpserver` | The importer, the commands and the MCP server use it; it reads changes as the report has them. |
| `internal/config` | `internal/providers/*`, `cli`, `mcpserver` | `config.Load` takes the providers' profiles from its caller. |
| `internal/selection`, `report` | `importer`, `internal/providers/*`, `cli`, `mcpserver` | The importer uses them, and they see listed resources through `harvest`. |
| `internal/discovery` | anything of infraharvest's but `harvest` | It reads listed resources through `harvest`; the importer drives it. |
| `internal/fsutil`, `ratelimit`, `logging`, `version`, `catalogue/schemameta`, `tftypes` | anything of infraharvest's | Leaf helpers: anything can use them without a cycle. |
| `internal/filelock` | anything of infraharvest's but `fsutil` | The lock checks its file with the leaf `fsutil`, as the output's other files are checked. |
| `pkg/cfn`, `pkg/cfn/hclname` | anything of infraharvest's but `pkg/cfn/hclname`, which imports nothing of infraharvest's | The two can move to a module of their own: the `depguard` rule keeps `internal/` out, and `TestNoInternalImports` the rest. |

Some of these would be an import cycle today as well, such as the engine importing `harvest` (`harvest` imports `catalogue`, which imports the engine), so the compiler stops them first; the rule keeps the direction if that chain goes. `depguard` reads the files golangci-lint compiles: the default build's, test files included, and the `integration`, `e2e` and `sandbox` ones (see [Build tags](#build-tags)). To add a rule, add a list under `linters.settings.depguard.rules` with a `desc` for each package it denies, then import a denied package somewhere it applies, check that `golangci-lint run --enable-only depguard ./...` reports it, and take the import out.

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

Contributors put their own name and the year the file was created. `scripts/check-headers.sh` fails when a tracked Go file, shell script (`*.sh`) or workflow (`.github/workflows/*.yml`) lacks the `SPDX-License-Identifier` line: the `repository` job of the `tests` workflow runs it on every pull request, and the pre-commit hook on the staged ones, where it also checks that a new Go file starts with both lines. `make fast-headers` runs it on every file.

New shell scripts, workflows, YAML files and Makefiles carry the same two lines as comments, after the `#!` line in a script, or any other line that must come first, such as a `Dockerfile`'s `# syntax=` (`scripts/fast-checks.sh`, `Makefile`, `Dockerfile`). Markdown, JSON and data files carry none. A file without a header is covered by `REUSE.toml`, which gives it the same copyright and licence (the one exception is the schema metadata read out of the providers' code, which `REUSE.toml` marks MPL-2.0: see [Licences](catalogue.md#licences)), and the `repository` job of the `tests` workflow (`.github/workflows/test.yml`) runs [`reuse lint`](https://github.com/fsfe/reuse-tool) on every pull request. Text that quotes a licence line without being one, such as the header examples on this page, goes between `REUSE-IgnoreStart` and `REUSE-IgnoreEnd` comments.

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

- Anything that does I/O or may run long takes a `context.Context` as its first argument: `importer.ImportCalls(ctx, ...)`, every method of `engine.Terraform`.
- The context starts at the command (`c.Context()` in `internal/cli/providers/command.go`) and is passed down, so an interrupt stops the whole import; the sandbox's resume test relies on it. The command catches the interrupt or SIGTERM once (`importer.InterruptContext`, from `withEngineRun` in `internal/cli/run.go`), not each Import call, and lets go of it after the first, so a second one stops the process at once. The first signal is the cause of the context's cancellation (`importer.Interrupt`), and `importer.Interrupted` turns the error the command ends on into exit code 130 (SIGINT) or 143 (SIGTERM).
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
- `-tags minimal` builds no provider; `-tags minimal,aws` builds AWS only. The `build-tags` job of the `tests` workflow checks both, and that a build links no SDK of a cloud it leaves out (`go list -deps -tags minimal,aws .`): what links a cloud's SDK, such as the store `--managed-state` reads that cloud's state with (`internal/managed/stores/*`), is set by the provider's own file. Locally, `make test PKG=./internal/cli/... TAGS=minimal,aws` compiles one SDK instead of three.
- Tests that need more than the Go toolchain have their own tag: `integration` (the engine against real Terraform), `e2e` (emulators, combined with a provider: `e2e && (!minimal || azure)`), and `sandbox` (a real AWS account, Azure subscription or Google Cloud project, run only by hand). golangci-lint compiles all three with every provider (`run.build-tags` in `.golangci.json`), the one build that holds every file: under `minimal,<provider>`, the e2e helpers only the other providers' tests call read as unused. `make lint TAGS=...` replaces those tags with yours.

## Tests

A bug fix comes with a test that fails without it. New behaviour comes with tests of it.

- **Table-driven**, with the inputs and expectations side by side and a failure message that names the call: `t.Errorf("Label(%q): got %q, want %q", name, got, want)` (`internal/engine/names_test.go`).
- **Fakes, not mocks.** Depend on the small interface you use and fake it: `engine.Terraform` is the part of `tfexec` the engine calls, faked by `fakeTerraform` (`internal/engine/engine_test.go`); `mcpserver.Runner` by `fakeRunner`; the engine's writes into a root, behind the importer's `callTools`, by setting the field a test needs.
- **AWS listers run against a fake API**: `useFakeAPI(t, handler)` (`internal/providers/aws/fakeapi_test.go`) sends the SDK's requests to a function that switches on the operation and returns the response body, including error bodies shaped like AWS's. Test the error paths, not only the happy one (`TestWafv2ReportsOtherAssociationErrors`). Azure and Google listers fake the transport or the pager the same way (`internal/providers/azure/network_listers_test.go`, `internal/providers/google/cloud_asset_test.go`).
- Helpers call `t.Helper()`; tests restore what they change with `t.Cleanup`, and write files under `t.TempDir()`.
- **Golden files** (`testdata/*.golden.*`) record output that is long or easier to review as a file: the catalogue's import blocks, the harvest core's behaviour, the command-line contract, the CloudFormation conversions of `pkg/cfn`, and the generated references (`docs/reference/cli.md`, `docs/reference/services.md`). Tests take `-update` to rewrite them. Don't edit a golden file by hand: run the `golden` task of the `remote` workflow (`make ci-golden PKG=<package>`), download what it rewrote (`make ci-download RUN=<id>`), and review the diff before committing it.
- **Characterization before a rewrite**: record what the code does today, in its own pull request, before changing how it does it (see [Rewriting code](#rewriting-code)).
- The emulator round trips in `e2e/` must keep planning with no changes on both Terraform and OpenTofu. To cover a new service, add it to the emulator's fixture as [e2e/README.md](https://github.com/IgnatG/infraharvest/blob/main/e2e/README.md) describes.

## Formatting and lint

- `gofmt -s`, and imports in three groups: the standard library, third-party modules, then this module's packages (`internal/engine/engine.go`). The pre-commit hook checks the grouping with `goimports -local github.com/IgnatG/infraharvest`. CI doesn't check the third group yet: `.golangci.json` gives the `goimports` formatter no `local-prefixes`.
- golangci-lint runs with `.golangci.json` (`depguard`, which holds the [dependency directions](#dependency-directions), `gocritic`, `revive`, `unconvert`, `unparam`, and the `gofmt` and `goimports` formatters) on the whole repository for every pull request and after each merge to `main`, in MegaLinter. Any finding fails the check, wherever it is, not only on changed lines.
- [MegaLinter](https://megalinter.io) runs every linter CI runs, in the `megalinter` workflow, and reports in one comment on the pull request and in the run's summary (only the summary after a merge). `.mega-linter.yml` lists the linters and says why the others are off: golangci-lint, actionlint and zizmor (workflows), shellcheck, markdownlint, lychee (links), hadolint, editorconfig-checker, yamllint, v8r (schemas), jsonlint, checkov, osv-scanner, betterleaks and trufflehog. `make ci-lint` runs it on a branch; `npx mega-linter-runner --flavor go -e GOTOOLCHAIN=auto` runs it locally, with Docker.
- Fix findings rather than silencing them. New code has no `//nolint`; the few there are mark legacy types due to be rewritten.

## Dependencies

- Every new module adds to a build that is already heavy: prefer the standard library and the SDKs already in `go.mod`, and say in the pull request why a new one is worth it.
- After changing imports or `go.mod`, run `go mod tidy` on GitHub: `make ci-tidy`, or push a branch named `tidy/<anything>`, then `make ci-download RUN=<id> NAME=go-mod` and commit `go.mod` and `go.sum`. The `tests` workflow fails when they aren't tidy.
- GitHub Actions are pinned to a full commit SHA with the version in a trailing comment, and tools downloaded in workflows to a version and a SHA-256 digest. Dependabot proposes updates weekly.
- `govulncheck` runs on the built binary on each pull request, and on each merge to `main`, and weekly in the `govulncheck` workflow; a finding fails unless `.github/govulncheck-allowlist.txt` covers it, with the reason.

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
- The description says what changes and why, how it was tested, and ends with whether a sandbox ran and why: they run by hand (`sandbox-aws`, `sandbox-azure`, `sandbox-google`), only for changes to that cloud's listers or permissions template, the catalogue, the engine, the adapters or the sandbox itself.
- A pull request merges once every check is green.

## Releases

The `release` workflow publishes a release from `main`: it runs the tests in a job with a read-only token, then tags the commit and has GoReleaser (`.goreleaser.yaml`) build the archives, the signed checksum file, the SBOMs and the signed container image into a draft release, attests the build provenance, and only then publishes the draft. If a step after the tag fails, it deletes the draft and the tag, so the same version can be released again. One release runs at a time. The release notes are generated: GitHub lists the pull requests merged since the previous release, so their titles (the squashed Conventional Commits) are the changelog, and GoReleaser adds how to install and verify the release below them.

1. Run `gh workflow run release.yml --ref main -f version=v0.2.0`. A version with a suffix, such as `v0.2.0-rc.1`, is published as a prerelease, which GitHub never counts as the latest: the install script and the image's `latest` tag skip it.
2. After the release, open a pull request that sets `devVersion` in `internal/version/version.go` to the next version with `-dev` (`v0.3.0-dev`), so builds from `main` don't report the version just released or one before it.

## Checks

| Check | Before committing | On GitHub |
|---|---|---|
| `gofmt -s`, import grouping | pre-commit hook, `make fast` | `megalinter` (golangci-lint's formatters) |
| Licence headers, names, `reuse lint`, a single root commit | pre-commit hook, `make fast` (not `reuse lint` or the root commit) | `tests` (`repository`) |
| Shell scripts, workflows | pre-commit hook (shellcheck, actionlint) | `megalinter` (shellcheck, actionlint, zizmor) |
| Markdown | pre-commit hook (markdownlint-cli2) | `megalinter` (markdownlint, lychee on links within the repository), `links` (external links, not required), `docs` (strict MkDocs build) |
| YAML, JSON, Dockerfile, Terraform, secrets, vulnerable dependencies | none | `megalinter` |
| Commit message | `commit-msg` hook | the pull request title, at review |
| golangci-lint | `make lint` (compiles) | `megalinter` on every pull request and merge to `main`; `make ci-lint` on a branch |
| `go vet`, `go test` | `make vet`, `make test` (compile) | `tests` on every pull request; `make ci-check PKG=...` on a branch |
| Golden files | `make golden` (compiles) | `make ci-golden PKG=...` |
| `go mod tidy` | none | `make ci-tidy`; `tests` checks it |
| Benchmarks, CPU profiles | `go test -bench` (compiles) | `make ci-bench BENCH=... BASE=<sha>`: the `bench` task of `remote` profiles the benchmarks and, with `BASE`, compares them with benchstat |
| Schema metadata (defaults and argument constraints read out of the providers' code) | none: it builds each provider | `make ci-schema-metadata` (the `schema-metadata` task of `remote`), then `make ci-download NAME=schema-metadata`; the weekly `schema-metadata` workflow opens a pull request when the newest releases change it (see [Regenerating](catalogue.md#regenerating)) |
| Emulator round trips, race detector, Windows, macOS, arm64 builds | none | `tests` on every pull request and after each merge to `main` |

The fast checks need only the tools they run (gofmt, goimports, shellcheck, actionlint, Node.js), each optional: the hook skips a check whose tool is missing and says how to install it, and CI runs it anyway. Run the compiling checks locally only if your machine can take it; otherwise push the branch and use the `ci-*` targets, which need the [GitHub CLI](https://cli.github.com/). `make help` lists every target.
