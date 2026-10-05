### Use with Vultr

Example:

```sh
export VULTR_API_KEY=[VULTR_API_KEY]
infraharvest import vultr --all --resources=server
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover vultr` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported Vultr resources:

*   `bare_metal_server`
    * `vultr_bare_metal_server`
*   `block_storage`
    * `vultr_block_storage`
*   `dns_domain`
    * `vultr_dns_domain`
    * `vultr_dns_record`
*   `firewall_group`
    * `vultr_firewall_group`
    * `vultr_firewall_rule`
*   `network`
    * `vultr_network`
*   `reserved_ip`
    * `vultr_reserved_ip`
*   `server`
    * `vultr_server`
*   `snapshot`
    * `vultr_snapshot`
*   `ssh_key`
    * `vultr_ssh_key`
*   `startup_script`
    * `vultr_startup_script`
*   `user`
    * `vultr_user`
