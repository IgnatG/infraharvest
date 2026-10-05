### Use with [Mikrotik](https://wiki.mikrotik.com/wiki/Manual:TOC)

This provider uses the [terraform-provider-mikrotik](https://github.com/ddelnano/terraform-provider-mikrotik). Its listers were built by [Dom Del Nano](https://github.com/ddelnano) for Terraformer.

Example:

```sh
## Warning! You should not expose your mikrotik creds through your bash history. Export them to your shell in a safe way when doing this for real!

MIKROTIK_HOST=router-hostname:8728 MIKROTIK_USER=username MIKROTIK_PASSWORD=password infraharvest import mikrotik --all --resources=dhcp_lease

# Import only static IPs
MIKROTIK_HOST=router-hostname:8728 MIKROTIK_USER=username MIKROTIK_PASSWORD=password infraharvest import mikrotik --all --resources=dhcp_lease --filter='Name=dynamic;Value=false'
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover mikrotik` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported mikrotik resources:

* `dhcp_lease`
  * `mikrotik_dhcp_lease`
