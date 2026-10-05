### Use with Fastly

Example:

```sh
export FASTLY_API_KEY=[FASTLY_API_KEY]
export FASTLY_CUSTOMER_ID=[FASTLY_CUSTOMER_ID]
infraharvest import fastly --all --resources=service_v1,user
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover fastly` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported Fastly resources:

*   `service_v1`
    * `fastly_service_acl_entries_v1`
    * `fastly_service_compute`
    * `fastly_service_dictionary_items_v1`
    * `fastly_service_dynamic_snippet_content_v1`
    * `fastly_service_v1`
*   `tls_subscription`
    * `fastly_tls_subscription`
*   `user`
    * `fastly_user_v1`
