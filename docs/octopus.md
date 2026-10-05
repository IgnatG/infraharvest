### Use with OctopusDeploy

Example:

```sh
export OCTOPUS_CLI_SERVER=http://localhost:8081/
export OCTOPUS_CLI_API_KEY=API-XXXXXXXXXXXXXXXXXXXXXXXXXX

infraharvest import octopusdeploy --all --resources=tagsets
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover octopusdeploy` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported OctopusDeploy resources:

* `accounts`
  * `octopusdeploy_account`
* `certificates`
  * `octopusdeploy_certificate`
* `environments`
  * `octopusdeploy_environment`
* `feeds`
  * `octopusdeploy_feed`
* `libraryvariablesets`
  * `octopusdeploy_library_variable_set`
* `lifecycles`
  * `octopusdeploy_lifecycle`
* `projects`
  * `octopusdeploy_project`
* `projectgroups`
  * `octopusdeploy_project_group`
* `projecttriggers`
  * `octopusdeploy_project_deployment_target_trigger`
* `tagsets`
  * `octopusdeploy_tag_set`
