### Use with Cloudflare

The Cloudflare listers were written for Cloudflare provider 3.x and record its resource types. infraharvest generates configuration with the newest provider release, and later major releases renamed or removed some of these types (provider 5 replaces `cloudflare_record` with `cloudflare_dns_record`, for example). Terraform rejects resources of those types, and the report lists them.

Example using a Cloudflare API Key and corresponding email:

```sh
export CLOUDFLARE_API_KEY=[CLOUDFLARE_API_KEY]
export CLOUDFLARE_EMAIL=[CLOUDFLARE_EMAIL]
export CLOUDFLARE_ACCOUNT_ID=[CLOUDFLARE_ACCOUNT_ID]
infraharvest import cloudflare --all --resources=firewall,dns
```

or using a Cloudflare API Token:

```sh
export CLOUDFLARE_API_TOKEN=[CLOUDFLARE_API_TOKEN]
export CLOUDFLARE_ACCOUNT_ID=[CLOUDFLARE_ACCOUNT_ID]
infraharvest import cloudflare --all --resources=firewall,dns
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover cloudflare` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported Cloudflare services:

* `access`
  * `cloudflare_access_application`
* `account_member`
  * `cloudflare_account_member`
* `dns`
  * `cloudflare_record`
  * `cloudflare_zone`
* `firewall`
  * `cloudflare_access_rule`
  * `cloudflare_filter`
  * `cloudflare_firewall_rule`
  * `cloudflare_rate_limit`
  * `cloudflare_zone_lockdown`
* `page_rule`
  * `cloudflare_page_rule`
