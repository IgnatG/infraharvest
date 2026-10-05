### Use with PagerDuty

Example:

```sh
export PAGERDUTY_TOKEN=YOUR_PAGERDUTY_TOKEN   # or pass --token=YOUR_PAGERDUTY_TOKEN
infraharvest import pagerduty --all --resources=team,schedule,user
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover pagerduty` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

Instructions to obtain a Auth Token: https://developer.pagerduty.com/docs/rest-api-v2/authentication/

List of supported PagerDuty resources:

* `business_service`
  * `pagerduty_business_service`
* `escalation_policy`
  * `pagerduty_escalation_policy`
* `ruleset`
  * `pagerduty_ruleset`
  * `pagerduty_ruleset_rule`
* `schedule`
  * `pagerduty_schedule`
* `service`
  * `pagerduty_service`
  * `pagerduty_service_event_rule`
* `team`
  * `pagerduty_team`
  * `pagerduty_team_membership`
* `user`
  * `pagerduty_user`
