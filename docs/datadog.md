# Use with [Datadog](https://www.datadoghq.com/)

This provider uses the [terraform-provider-datadog](https://registry.terraform.io/providers/DataDog/datadog/latest).

## Usage

```bash
export DATADOG_API_KEY=<API key>   # https://docs.datadoghq.com/account_management/api-app-keys/
export DATADOG_APP_KEY=<APP key>   # https://docs.datadoghq.com/account_management/api-app-keys/
export DATADOG_HOST=<API host>     # such as https://api.datadoghq.eu, see https://docs.datadoghq.com/getting_started/site/#access-the-datadog-site

infraharvest import datadog --all --resources="*"
```

`--api-key`, `--app-key` and `--api-url` can be given instead of the environment variables. You can also import only certain kinds of resources, such as `--resources=dashboard`.

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover datadog` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

Each generated root's `README.md` lists what was imported and the steps left to take. See [Output](../README.md#output).

### Filtering Resources

You can use the `filter` argument to restrict the import of Terraform resources.

Filtering based on Tags follows the convention `--filter="Name=tags;Value='your tag'"`.

```bash
# Import monitors based on multiple tags
infraharvest import datadog --all --resources=monitor --filter="Name=tags;Value='foo:bar'" --filter="Name=tags;Value='env:production'"

# Import monitor where tag doesn't include colon
infraharvest import datadog --all --resources=monitor --filter="Name=tags;Value=anExampleTag"
```

Filtering based on resource ID:

```bash
# Import dashboard based on the dashboard ID
infraharvest import datadog --all --resources=dashboard --filter=dashboard=some-id

# Import based on multiple resource IDs
infraharvest import datadog --all --resources=monitor --filter=monitor=id1:id2:id4
```

Tag filters are order specific. For example, if your monitor has tags (in the order) `atag: atagvalue`, `foo:bar` but you filter for `--filter="Name=tags;Value='foo:bar'" --filter="Name=tags;Value='atag: atagvalue'"`, the monitor would not be imported.

## Supported Datadog resources

*   `dashboard`
    * `datadog_dashboard`
*   `dashboard_json`
    * `datadog_dashboard_json`
*   `dashboard_list`
    * `datadog_dashboard_list`
*   `downtime`
    * `datadog_downtime`
*   `integration_aws`
    * `datadog_integration_aws`
*   `integration_aws_lambda_arn`
    * `datadog_integration_aws_lambda_arn`
*   `integration_aws_log_collection`
    * `datadog_integration_aws_log_collection`
*   `integration_azure`
    * `datadog_integration_azure`
        * **_NOTE:_** Sensitive field `client_secret` is not generated and needs to be manually set
*   `integration_gcp`
    * `datadog_integration_gcp`
        * **_NOTE:_** Sensitive fields `private_key, private_key_id, client_id` is not generated and needs to be manually set
*   `integration_pagerduty`
    * `datadog_integration_pagerduty`
*   `integration_pagerduty_service_object`
    * `datadog_integration_pagerduty_service_object`
*   `integration_slack_channel`
    * `datadog_integration_slack_channel`
        * **_NOTE:_** Importing resource requires resource ID or `account_name` to be passed via [Filter][1] option
*   `logs_archive`
    * `datadog_logs_archive`
*   `logs_archive_order`
    * `datadog_logs_archive_order`
*   `logs_custom_pipeline`
    * `datadog_logs_custom_pipeline`
*   `logs_index`
    * `datadog_logs_index`
*   `logs_index_order`
    * `datadog_logs_index_order`
*   `logs_integration_pipeline`
    * `datadog_logs_integration_pipeline`
*   `logs_pipeline_order`
    * `datadog_logs_pipeline_order`
*   `metric_metadata`
    * `datadog_metric_metadata`
        * **_NOTE:_** Importing resource requires resource ID's to be passed via [Filter][1] option
*   `monitor`
    * `datadog_monitor`
*   `role`
    * `datadog_role`
*   `security_monitoring_default_rule`
    * `datadog_security_monitoring_default_rule`
*   `security_monitoring_rule`
    * `datadog_security_monitoring_rule`
*   `service_level_objective`
    * `datadog_service_level_objective`
*   `synthetics_global_variable`
    * `datadog_synthetics_global_variable`
        * **_NOTE:_** Importing resource requires resource ID's to be passed via [Filter][1] option
*   `synthetics_private_location`
    * `datadog_synthetics_private_location`
*   `synthetics_test`
    * `datadog_synthetics_test`
*   `user`
    * `datadog_user`

[1]: ../README.md#filtering
