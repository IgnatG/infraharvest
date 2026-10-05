### Use with Linode

Example:

```sh
export LINODE_TOKEN=[LINODE_TOKEN]
infraharvest import linode --all --resources=instance
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover linode` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported Linode resources:

*   `domain`
    * `linode_domain`
    * `linode_domain_record`
*   `image`
    * `linode_image`
*   `instance`
    * `linode_instance`
*   `nodebalancer`
    * `linode_nodebalancer`
    * `linode_nodebalancer_config`
    * `linode_nodebalancer_node`
*   `rdns`
    * `linode_rdns`
*   `sshkey`
    * `linode_sshkey`
*   `stackscript`
    * `linode_stackscript`
*   `token`
    * `linode_token`
*   `volume`
    * `linode_volume`
