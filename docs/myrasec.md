### Use with Myra Security

Example using a Myra Security API Key and corresponding Token:

```sh
export MYRASEC_API_SECRET=[MYRASEC_API_SECRET]
export MYRASEC_API_KEY=[MYRASEC_API_KEY]
infraharvest import myrasec --all --resources=domain
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover myrasec` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported Myra Security services:

* `cache_setting`
  * `myrasec_cache_setting`
* `dns_record`
  * `myrasec_dns_record`
* `domain`
  * `myrasec_domain`
* `error_page`
  * `myrasec_error_page`
* `ip_filter`
  * `myrasec_ip_filter`
* `maintenance`
  * `myrasec_maintenance`
* `ratelimit`
  * `myrasec_ratelimit`
* `redirect`
  * `myrasec_redirect`
* `settings`
  * `myrasec_settings`
* `waf_rule`
  * `myrasec_waf_rule`
