### Use with Launchdarkly

Example:

```sh
export LAUNCHDARKLY_ACCESS_TOKEN=[LAUNCHDARKLY_ACCESS_TOKEN]
infraharvest import launchdarkly --all --resources=project
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover launchdarkly` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported LaunchDarkly resources:

*   `project`
    * `launchdarkly_project`
