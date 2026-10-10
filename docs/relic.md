### Use with New Relic

Example:

```sh
infraharvest import newrelic --all --resources=alert,infra,synthetics --api-key=NRAK-XXXXXXXX --account-id=XXXXX
```

The API key, account ID and region default to `NEW_RELIC_API_KEY`, `NEW_RELIC_ACCOUNT_ID` and `NEW_RELIC_REGION`. Set `--region=EU` (or `JP`) for accounts outside the US; the generated provider block gets the account ID and region, and the API key stays in `NEW_RELIC_API_KEY`. `tags` needs the account ID: it imports each synthetic monitor and alert condition with tags as one `newrelic_entity_tags`, by the entity's GUID.

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover newrelic` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported New Relic resources:

*   `alert`
    * `newrelic_alert_channel`
    * `newrelic_alert_condition`
    * `newrelic_alert_policy`
    * `newrelic_nrql_alert_condition`
*   `alert_channel`
    * `newrelic_alert_channel`
*   `alert_condition`
    * `newrelic_alert_condition`
    * `newrelic_nrql_alert_condition`
*   `alert_policy`
    * `newrelic_alert_policy`
*   `infra`
    * `newrelic_infra_alert_condition`
*   `synthetics`
    * `newrelic_synthetics_monitor`
*   `tags`
    * `newrelic_entity_tags`
