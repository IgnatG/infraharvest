# Contributing

## Workflow

- Branch from `main` and open a pull request. CI runs on every pull request.
- Keep pull requests focused. A bug fix includes a test that fails without the fix.

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```text
fix(aws): paginate ListQueues
feat(selection): add tag filters
ci: pin actions to commit SHAs
```

Common types: `feat`, `fix`, `refactor`, `test`, `docs`, `ci`, `build`, `chore`.

## Checks

CI on each pull request:

- `go mod tidy -diff`, a build of the binary and `go test ./...` on Linux and macOS.
- Tests of OS-sensitive packages on Windows.
- `govulncheck` in binary mode.
- golangci-lint on changed lines.

The module is large (44 providers), so building or testing all of it needs several GB of RAM. To check selected packages on GitHub instead of locally:

```sh
gh workflow run check --ref <branch> -f packages="./terraformutils/... ./providers/aws/..." -f os=ubuntu-latest
gh run watch
```

## Dependencies

- Pin GitHub Actions to a full commit SHA, with the version in a trailing comment.
- Dependabot proposes updates weekly.

## Licence

By contributing, you agree that your contributions are licensed under the repository's licence (see [LICENSE](LICENSE)).
