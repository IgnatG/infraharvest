### Use with Equinix Metal

Example:

```sh
export METAL_AUTH_TOKEN=[METAL_AUTH_TOKEN]
export PACKET_PROJECT_ID=[PROJECT_ID]
infraharvest import metal --all --resources=volume,device
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover metal` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported Equinix Metal resources:

*   `device`
    * `metal_device`
*   `spotmarketrequest`
    * `metal_spot_market_request`
*   `sshkey`
    * `metal_ssh_key`
*   `volume`
    * `metal_volume`
