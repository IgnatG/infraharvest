### Use with New Relic

Example:

```sh
infraharvest import newrelic --all --resources=alert,infra,synthetics --api-key=NRAK-XXXXXXXX --account-id=XXXXX
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover newrelic` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported New Relic resources:

*   `alert`
    * `newrelic_alert_channel`
    * `newrelic_alert_condition`
    * `newrelic_alert_policy`
    * `newrelic_nrql_alert_condition`
*   `alertchannel`
    * `newrelic_alert_channel`
*   `alertcondition`
    * `newrelic_alert_condition`
    * `newrelic_nrql_alert_condition`
*   `alertpolicy`
    * `newrelic_alert_policy`
*   `infra`
    * `newrelic_infra_alert_condition`
*   `synthetics`
    * `newrelic_synthetics_monitor`
*   `tags`
    * `newrelic_entity_tags`
