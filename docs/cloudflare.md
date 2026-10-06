### Use with Cloudflare

The listers record the resource types of Cloudflare provider 5, with the IDs Terraform imports them with (`<zone_id>/<record_id>` for a DNS record, for example).

`CLOUDFLARE_ACCOUNT_ID` is required for `account_member`. With it, `firewall` also imports the account's IP access rules.

Cloudflare has retired the Firewall Rules, Filters and previous Rate Limiting APIs; they answer `410 Gone`. When they do, `firewall` logs that it skipped them and imports the zone's other resources.

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
  * `cloudflare_zero_trust_access_application` (zone-level applications)
* `account_member`
  * `cloudflare_account_member`
* `dns`
  * `cloudflare_dns_record`
  * `cloudflare_zone`
* `firewall`
  * `cloudflare_access_rule`
  * `cloudflare_filter`
  * `cloudflare_firewall_rule`
  * `cloudflare_rate_limit`
  * `cloudflare_zone_lockdown`
* `page_rule`
  * `cloudflare_page_rule`
