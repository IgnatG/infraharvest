### Use with Cloudflare

Terraformer can only load providers that serve Terraform plugin protocol 5.
Cloudflare provider 4.0 and later use protocol 6, so install a 3.x release
(3.12.2 is reported to work):

```hcl
terraform {
  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 3.12"
    }
  }
}
```

With a newer provider, terraformer exits with an error saying the provider
uses a plugin protocol it cannot load.

Example using a Cloudflare API Key and corresponding email:
```
export CLOUDFLARE_API_KEY=[CLOUDFLARE_API_KEY]
export CLOUDFLARE_EMAIL=[CLOUDFLARE_EMAIL]
export CLOUDFLARE_ACCOUNT_ID=[CLOUDFLARE_ACCOUNT_ID]
 ./infraharvest import cloudflare --resources=firewall,dns
```

or using a Cloudflare API Token:

```
export CLOUDFLARE_API_TOKEN=[CLOUDFLARE_API_TOKEN]
export CLOUDFLARE_ACCOUNT_ID=[CLOUDFLARE_ACCOUNT_ID]
 ./infraharvest import cloudflare --resources=firewall,dns
```

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
