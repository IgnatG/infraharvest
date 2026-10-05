### Use with NS1

Example:

```sh
export NS1_APIKEY=[NS1_APIKEY]
infraharvest import ns1 --all --resources=zone,monitoringjob,team
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover ns1` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported NS1 resources:

*   `monitoringjob`
    * `ns1_monitoringjob`
*   `team`
    * `ns1_team`
*   `zone`
    * `ns1_zone`
