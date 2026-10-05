# Use with Azure DevOps

Supports access via [Personal Access Token](https://registry.terraform.io/providers/microsoft/azuredevops/latest/docs/guides/authenticating_using_the_personal_access_token).

## Example

```sh
export AZDO_ORG_SERVICE_URL="https://dev.azure.com/<Your Org Name>"
export AZDO_PERSONAL_ACCESS_TOKEN="<Personal Access Token>"

infraharvest import azuredevops --all --resources="*"
infraharvest import azuredevops --all --resources=project,git_repository
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover azuredevops` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

## List of supported Azure DevOps resources

* `git_repository`
  * `azuredevops_git_repository`
* `group`
  * `azuredevops_group`
* `project`
  * `azuredevops_project`

## Notes

Since [Terraform Provider for Azure DevOps](https://github.com/microsoft/terraform-provider-azuredevops) `version 0.17`.
