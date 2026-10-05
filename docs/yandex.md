### Use with Yandex Cloud

Example:

```sh
export YC_TOKEN=[YANDEX_CLOUD_OAUTH_OR_IAM_TOKEN]
infraharvest import yandex --all --resources=subnet --folder_ids=<comma-separated folder IDs>
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover yandex` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported Yandex resources:

*   `disk`
    * `yandex_compute_disk`
*   `instance`
    * `yandex_compute_instance`
*   `network`
    * `yandex_vpc_network`
*   `subnet`
    * `yandex_vpc_subnet`

