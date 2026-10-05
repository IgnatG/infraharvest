### Use with GitHub

Example:

```sh
export GITHUB_TOKEN=YOUR_TOKEN   # or pass --token=YOUR_TOKEN
infraharvest import github --all --owner=YOUR_ORGANIZATION --resources=repositories
infraharvest import github --all --owner=YOUR_ORGANIZATION --resources=repositories --filter=repository=id1:id2:id4
infraharvest import github --all --owner=YOUR_ORGANIZATION --resources=repositories --base-url=https://your-enterprise-github-url
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover github` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

Supports only organizational resources. List of supported resources:

*   `members`
    * `github_membership`
*   `organization_blocks`
    * `github_organization_block`
*   `organization_projects`
    * `github_organization_project`
*   `organization_webhooks`
    * `github_organization_webhook`
*   `repositories`
    * `github_branch_protection`
    * `github_repository`
    * `github_repository_collaborator`
    * `github_repository_deploy_key`
    * `github_repository_webhook`
*   `teams`
    * `github_team`
    * `github_team_membership`
    * `github_team_repository`
*   `user_ssh_keys`
    * `github_user_ssh_key`

Notes:
* The GitHub API doesn't return webhook secrets. If a webhook uses a secret, the generated configuration doesn't have it, and you need to set it before applying.
