### Use with Mackerel

Example:

```bash
export MACKEREL_API_KEY=YOUR_MACKEREL_API_KEY   # or pass --api-key=YOUR_MACKEREL_API_KEY
infraharvest import mackerel --all --resources=service
infraharvest import mackerel --all --resources=service --filter=service=name1:name2:name4
infraharvest import mackerel --all --resources=aws_integration --filter=aws_integration=id1:id2:id4
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover mackerel` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

List of supported Mackerel services:

*   `alert_group_setting`
    * `mackerel_alert_group_setting`
*   `aws_integration`
    * `mackerel_aws_integration`
        * Sensitive field `secret_key` is not generated and needs to be manually set
        * Sensitive field `external_id` is not generated and needs to be manually set
*   `channel`
    * `mackerel_channel`
*   `downtime`
    * `mackerel_downtime`
*   `monitor`
    * `mackerel_monitor`
*   `notification_group`
    * `mackerel_notification_group`
*   `role`
    * `mackerel_role`
*   `service`
    * `mackerel_service`