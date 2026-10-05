# Use with [Opal](https://opal.dev)

This provider uses the [opalsecurity/opal](https://registry.terraform.io/providers/opalsecurity/opal/latest) Terraform provider.

## Usage

```bash
export OPAL_AUTH_TOKEN=Your token from https://app.opal.dev/settings#api
# If you are running an on-prem installation, you will need to provide a base url as well:
# export OPAL_BASE_URL=https://my.opal.com

infraharvest import opal --all --resources="*"
```

You can also specify only certain kinds of resources to import as well, i.e. `--resources=owner`.

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover opal` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

Note that we currently do not support the `--filter` flag.

Each generated root's `README.md` lists what was imported and the steps left to take. See [Output](../README.md#output).

## Supported Opal resources:

*   `group`
    * `opal_group`
*   `message_channels`
    * `opal_message_channels`
*   `on_call_schedules`
    * `opal_on_call_schedules`
*   `owner`
    * `opal_owner`
*   `resource`
    * `opal_resource`
