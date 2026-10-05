### Use with [Xen Orchestra](https://xen-orchestra.com/)

This provider uses the [terraform-provider-xenorchestra](https://github.com/ddelnano/terraform-provider-xenorchestra). Its listers were built for Terraformer by [Dom Del Nano](https://github.com/ddelnano) on behalf of [Vates SAS](https://vates.fr/) who is sponsoring Dom to work on the project.

Example:

```sh
## Warning! You should not expose your xenorchestra creds through your bash history. Export them to your shell in a safe way when doing this for real!

XOA_URL=ws://your-xenorchestra-domain XOA_USER=username XOA_PASSWORD=password infraharvest import xenorchestra --all --resources=acl
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover xenorchestra` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported xenorchestra resources:

* `acl`
  * `xenorchestra_acl`
* `resource_set`
  * `xenorchestra_resource_set`
