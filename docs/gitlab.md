### Use with GitLab

Example:

```sh
export GITLAB_TOKEN=YOUR_TOKEN   # or pass --token=YOUR_TOKEN
infraharvest import gitlab --all --group=GROUP_TO_IMPORT --resources=projects
infraharvest import gitlab --all --group=GROUP_TO_IMPORT --resources=groups --base-url=https://your-self-hosted-gitlab-domain/api/v4
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover gitlab` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported resources:

* `groups`
  * `gitlab_group_membership`
  * `gitlab_group_variable`
* `projects`
  * `gitlab_branch_protection`
  * `gitlab_project`
  * `gitlab_project_membership`
  * `gitlab_project_value`
  * `gitlab_tag_protection`
