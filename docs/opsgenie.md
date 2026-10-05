### Use with Opsgenie

Example:

```sh
export OPSGENIE_API_KEY=YOUR_API_KEY   # or pass --api-key=YOUR_API_KEY
infraharvest import opsgenie --all --resources=team,user
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover opsgenie` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported Opsgenie services:

*   `team`
    * `opsgenie_team`
*   `user`
    * `opsgenie_user`
