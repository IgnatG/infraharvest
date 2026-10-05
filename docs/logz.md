### Use with Logz.io

Example:

```sh
# Import Logz.io alerts and alert notification endpoints
LOGZIO_API_TOKEN=foobar LOGZIO_BASE_URL=https://api-eu.logz.io infraharvest import logzio --all --resources=alerts,alert_notification_endpoints
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover logzio` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported Logz.io resources:

*   `alert_notification_endpoints`
    * `logzio_endpoint`
*   `alerts`
    * `logzio_alert`
