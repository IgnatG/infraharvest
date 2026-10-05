### Use with [Commercetools](https://commercetools.com/de/)

This provider uses the [terraform-provider-commercetools](https://github.com/labd/terraform-provider-commercetools). Its listers were built by [Dustin Deus](https://github.com/StarpTech) for Terraformer.

Example:

Export required variables:

```bash
export CTP_PROJECT_KEY=key
export CTP_CLIENT_ID=foo
export CTP_CLIENT_SECRET=bar
export CTP_CLIENT_SCOPE=scope
```

Export optional variables in case default values are not appropriate:

```bash
export CTP_BASE_URL=base_url # default: https://api.sphere.io
export CTP_TOKEN_URL=token_url # default: https://auth.sphere.io
```

Run infraharvest:

```bash
infraharvest import commercetools --all --resources=types
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover commercetools` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported [commercetools](https://commercetools.com/de/) resources:

- `api_extension`
  - `commercetools_api_extension`
- `channel`
  - `commercetools_channel`
- `custom_object`
  - `commercetools_custom_object`
- `product_type`
  - `commercetools_product_type`
- `shipping_method`
  - `commercetools_shipping_method`
- `shipping_zone`
  - `commercetools_shipping_zone`
- `state`
  - `commercetools_state`
- `store`
  - `commercetools_store`
- `subscription`
  - `commercetools_subscription`
- `tax_category`
  - `commercetools_tax_category`
- `types`
  - `commercetools_type`
