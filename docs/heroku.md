### Use with Heroku

This utilizes [terraform-provider-heroku](https://registry.terraform.io/providers/heroku/heroku/latest).

Heroku organizes itself by apps. This importer tool is designed to capture complete apps with all their dependent resources like addons, domains, etc.

#### Apps by ID, Not Name

Apps must be identified by ID (UUID). Even though some resources may import successfully when filtering by app name, apps themselves must be identified by ID. To get an app ID, use Heroku CLI to get the top-level `id` property:

```
heroku apps:info --json --app=<NAME>
```

#### App Config Vars

An app's config vars may contain secrets. Review `config_vars` in the generated configuration, and move secrets into `sensitive_config_vars` before applying.

#### Builds

The imported configuration cannot build & launch apps in a new place. To launch apps that have been imported with infraharvest, one of the following is required:
* source pushed to the new Heroku apps, `git push heroku master` from each app's repo
* new apps added to an existing Heroku pipelines and promoted to, via the web dashbord or CLI
* new apps connected for GitHub deployments, via the web dashboard
* a [`heroku_build` resource](https://registry.terraform.io/providers/heroku/heroku/latest/docs/resources/build) added to the Terraform configuration.

#### Example

✏️  *Please replace angle-bracketed* `<VALUES>` *with your specific values.*

```sh
export HEROKU_API_KEY=<token>

# All team's apps
infraharvest import heroku --all --resources=app --team=<NAME>

# Specific app(s), by UUID
infraharvest import heroku --all --resources=app --filter=app=<ID>
infraharvest import heroku --all --resources=app --filter=app=<ID>:<ID2>:<ID3>

# Output directory
infraharvest import heroku --all --resources=app --filter=app=<ID> --path-pattern='{output}/{provider}/<DIRECTORY NAME>'

# All enabled features of HEROKU_API_KEY's Heroku account
infraharvest import heroku --all --resources=account_feature
```

`--all` imports everything the default selection includes. To review what will be imported first, run `infraharvest discover heroku` with the same flags, then import with `--selection=selection.yaml` instead of `--all` (see [Choosing what to import](../README.md#choosing-what-to-import)).

Heroku services with the terraform-provider-heroku resources they import:

*   `account_feature`
    * `heroku_account_feature`
*   `app`
    * `heroku_addon`
    * `heroku_addon_attachment` (includes attachments to other apps)
    * `heroku_app`
    * `heroku_app_feature`
    * `heroku_app_webhook`
    * `heroku_domain`
    * `heroku_drain`
    * `heroku_formation`
    * `heroku_ssl`
*   `pipeline`
    * `heroku_pipeline`
*   `pipeline_coupling`
    * `heroku_pipeline_coupling`
*   `team_collaborator`
    * `heroku_team_collaborator`
*   `team_member`
    * `heroku_team_member`

