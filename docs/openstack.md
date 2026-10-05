### Use with OpenStack

Example:

```sh
infraharvest import openstack --all --resources=compute,networking --regions=RegionOne
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover openstack` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported OpenStack services:

*   `blockstorage`
    * `openstack_blockstorage_volume_v1`
    * `openstack_blockstorage_volume_v2`
    * `openstack_blockstorage_volume_v3`
*   `compute`
    * `openstack_compute_instance_v2`
*   `networking`
    * `openstack_networking_secgroup_v2`
    * `openstack_networking_secgroup_rule_v2`
