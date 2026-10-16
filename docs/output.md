# Output

## Layout

Roots go under the `--out` directory, `<out>` below, which is `infraharvest-out` unless set. By default each root is one state boundary: `<out>/<provider>/<account>/<region>/`, with `global` for global services such as IAM. Azure uses the subscription and the resource group instead (`all` for the whole subscription), and Google Cloud the project and the region (`global` for what belongs to no region); see each cloud's page. `<provider>` is the command that imported it, `aws`, `azure` or `google`, whatever the Terraform provider: Azure roots use `azurerm`, and Google roots `google-beta` with `--beta`. `--layout` can change that, with `{account}` and `{region}` as well as `{out}`, `{provider}` and `{service}`.

With `--env-tags=Environment` (any tag or label keys, first match wins), each environment gets roots of its own, with their own state: `<out>/envs/<env>/<provider>/<account>/<region>/`, or wherever `{env}` is in `--layout`. A resource without the tag, such as a bucket's versioning or a subnet's route table association, goes with the tagged resource whose ID its import ID contains, a child import with its parent, and anything else to `shared`. References to what another root manages read it through a data source, where its type has one. Identical shapes in different environments share one generated local module, called from each root. Environments are opt-in: an earlier import's roots keep their paths, so `--incremental` never generates a resource twice.

## Files

Each output directory gets:

| File | Contents |
|---|---|
| `versions.tf` | `required_version` from the oldest Terraform release the configuration works with, within its major release: 1.5, which brought import blocks (`>= 1.5, < 2.0`), or the release a later feature it uses came with, such as write-only arguments (1.11), or the lowest version a module it calls requires. And the provider pinned to its newest minor release line (`~> 6.67`) |
| `providers.tf` | The provider configuration |
| `imports.tf` | One `import` block per resource. Delete it after the first `terraform apply` |
| `network.tf`, `s3.tf`, `iam.tf`, ... | The configuration Terraform generated for the resources, a file per service: the network's resources (VPCs, subnets, route tables, gateways, security groups) together in `network.tf`, the others in their service's file. A module call goes with the first resource it took |
| `variables.tf` | Only if there are secrets or values of other roots: a `sensitive` variable, with no default, for each secret value (see [Secrets](#secrets)). Set the variables before planning, from your secret store (AWS Secrets Manager, Azure Key Vault, Google Secret Manager) or with `TF_VAR_<name>`, keeping the values out of version control. Write-only arguments (`*_wo`) stay unset. Also a variable, with its value as the default, for each ID, ARN or DNS name of a resource another root manages that no data source reads (see [Tidying](#tidying)) |
| `.terraform.lock.hcl` | The provider version, the same for every directory of one import |
| `rejected.hcl` | Only if some resources couldn't be imported: their `import` block and generated configuration, under the errors Terraform reported. Terraform doesn't load this file. Fix a resource and move its import block into `imports.tf` and its resource into the configuration, or leave it out. Rejected resources make the import exit non-zero unless `--allow-partial` is set |

Each directory also gets a `README.md` with what was imported and the steps left to take, and the output directory gets a `.gitignore` for state, plans, `.terraform/` and `.tfvars` files, unless it already has one. Resource labels are described under [Resource labels](#resource-labels).

## Resource labels

The label of each resource block (the `NAME` in `resource "TYPE" "NAME"`) follows the [HashiCorp style guide](https://developer.hashicorp.com/terraform/language/style): lower case words joined by underscores, made from what a person knows the resource by rather than from its ID.

- **The name in the cloud:** a resource's name, or on AWS its `Name` tag where it has one; on Google Cloud, the resource's own name or display name (`google_pubsub_topic.orders`, not its full name), a record set's zone, name within the zone and type, and an IAM grant's member and role (`google_project_iam_member.deployer_run_invoker`). Words of a camelCase name stay apart (`AllowHttpApi` becomes `allow_http_api`), and a name that repeats itself, such as a cluster's name in front of a service named after the cluster, says it once.
- **Something human for what has no name:** an API Gateway route by its route key (`GET /hello` becomes `get_hello`), a stage by its name, an integration by the function or host it calls, a load balancer listener by its protocol and port (`http_80`), a VPC endpoint by its service (`s3`), a KMS key by its alias, an SNS subscription by its endpoint and protocol, a Route 53 record by its name in the zone and its type (`www_cname`), an Azure subnet's route table or security group association by the subnet (`app`).
- **What it belongs to, by its name:** where a resource is known by another's ID, the label uses that resource's name: a route table association or a route table without a `Name` tag after its subnet, an Elastic IP after its instance.
- **Without what the root already says:** a label drops the words of its resource type (`aws_sqs_queue.orders`, not `orders_queue`) and the prefix most of the root's names share, such as a project's name (`aws_ecs_service.web`, not `shop_prod_web`). A resource whose type the root has no other of is labelled `main`, and a module call built around it is named after its type (`module "vpc"`).

Labels are unique per type: a label two resources would share gets `_2`, `_3` and so on, in import ID order, so the same resources get the same labels on every run whatever order they are listed in. `--incremental` and `--resume` match resources by their import IDs, not their labels, and an incremental import never takes a label the root uses, nor labels a new resource `main` when the root already has one of its type.

## Secrets

infraharvest never writes a secret value into its output: not into the configuration, the report, the README or its logs. Terraform leaves out the values the provider marks sensitive, such as a `SecureString` SSM parameter's `value`, but writes all the others, and providers don't mark the values of maps and JSON documents. So infraharvest looks through every string of the configuration Terraform generated, nested maps, lists and JSON documents included, such as a Lambda function's environment variables, an ECS container definition, an App Service's app settings and connection strings, or a Cloud Run container's env and a VM's metadata. A value is a secret when:

- it has the format of a credential, wherever it is: a private key, a token with a known prefix (GitHub, GitLab, Slack, Stripe, Google API keys, ...), a JSON Web Token, a password in a URL or a connection string, an Azure storage or shared access key, a SAS signature;
- or, as the value of a key someone named, such as an environment variable, its name says it is one (`SECRET`, `TOKEN`, `PASSWORD`, `PASSWD`, `PWD`, `API_KEY`, `PRIVATE_KEY`, `ACCESS_KEY`, `CLIENT_SECRET`, `CONNECTION_STRING`, `CREDENTIAL`, ...), unless the name ends in what identifies a secret rather than holds it (`SECRET_ARN`, `TOKEN_URL`, `ACCESS_KEY_ID`);
- or it is random enough to be a key: 20 or more characters of hex or mixed-case base64, unless its name says it is a digest, a version or an ID (`SOURCE_HASH`, `IMAGE_TAG`).

ARNs, IDs and UUIDs, URLs without credentials, paths, numbers, booleans, text with spaces, short values, and references to secrets kept elsewhere (`@Microsoft.KeyVault(...)`, `{{resolve:...}}`, Secret Manager names) are left as they are, and so are tags and labels, but for credential formats. Each secret becomes a reference to a new `sensitive` variable without a default, named after the resource and the key (`var.aws_lambda_function_api_webhook_secret`), listed in `variables.tf`, the README and the report. A value inside a JSON string is replaced in place (`"${var.name}"`) where the JSON needs no escaping for it. infraharvest's own plans read the values from a private temporary file outside the output, deleted when the directory is done, so they stay clean. An AWS access key ID used as a tag key isn't a secret, but gets a warning in the README and the report.

The AWS provider marks every SSM parameter's `value` sensitive, but only a `SecureString`'s is secret: a `String` or `StringList` parameter's value is written into the configuration as it is (as `value`, which plans clean after import, where `insecure_value` would plan an update), unless it looks like a secret all the same, such as a parameter named `/app/db_password`.

## Tidying

Terraform writes every argument of a resource it generates configuration for, in alphabetical order. infraharvest tidies that into what a person would write, in this order, and keeps each step only if a new plan has each resource plan the change it had before, the same actions on the same attributes, not only the same totals (the layout, which changes nothing Terraform reads, needs none): defaults and empty values go, literals become references and data sources, shared tags and repeated identifiers become locals, clusters become module calls, and the result is laid out as HashiCorp's style guide has it.

Optional arguments that only repeat a default go: arguments set to `null`, optional arguments set to the provider's default, whatever it is (`false`, `0` and `""` as much as an IAM role's `path = "/"` or `max_session_duration = 3600`), and nested blocks left with nothing set (`ttl {}`). The providers' schemas don't say what their defaults are, so the plan decides: infraharvest leaves every candidate out at once, validates, which needs no cloud, and puts back what that rejects, such as one of two required alternatives, then plans and puts back the arguments of each resource whose plan would change, all of a resource's if its plan changes again. A few plans settle a whole directory. Computed arguments set to a value keep it: leaving one out never shows in the plan, so it could hide a real setting. The defaults the provider or the cloud gives those are in the [catalogue](catalogue.md) (`defaults`, by provider version where a major release changed them), such as a Google Cloud bucket's `storage_class = "STANDARD"`, a subscription's seven days of message retention, or a firewall rule's priority 1000; they go too, where the plan stays the same.

Empty values (`""`, `[]`, `{}`) of optional arguments go too, and so do nested blocks that set nothing but zero values (`access_logs { bucket = "", enabled = false }`), even where the provider computes the argument: an explicit `dns_servers = []` or `access_policy = []` would remove what a separate resource, such as `azurerm_virtual_network_dns_servers`, sets, and left out, the argument keeps what the cloud has. The catalogue's `keep_empty` keeps the few whose empty value the provider documents as a setting of its own.

Literals that are another imported resource's ID or ARN become references: `vpc_id = aws_vpc.main.id`, `role_arn = aws_iam_role.app.arn`. An argument named after a resource type refers to that resource by name, for example `bucket = aws_s3_bucket.logs.bucket`. Values several resources share are skipped, unless one is the others' parent (a bucket and its configuration resources). So are references that would make resources depend on each other in a loop.

The other values that identify a resource in the same directory become references too:

- **Short IDs and names**, where the argument names the resource's type, ignoring a suffix such as `_id` or `_name`: `rest_api_id = aws_api_gateway_rest_api.shop.id`, `log_group = aws_cloudwatch_log_group.fn.name`, an alarm dimension's `QueueName = aws_sqs_queue.orders.name`. An argument that takes IDs (`_id`, `_arn`) also refers to the one resource whose ID it is when the ID looks generated (six characters or more, with letters and digits): `parent_id = aws_api_gateway_rest_api.shop.root_resource_id`.
- **Attributes the provider computes** to identify a resource, preferring the one the argument names: `target_key_id = aws_kms_key.main.key_id`, `zone_id = aws_route53_zone.shop.zone_id`, a load balancer's `dns_name`, a distribution's `domain_name` and a bucket's `bucket_regional_domain_name`, as a CloudFront origin names it. An ECS service's `task_definition = "web:3"` becomes `"${aws_ecs_task_definition.web.family}:${aws_ecs_task_definition.web.revision}"`, as the provider reads it back.
- **IDs inside longer strings**, matched only as whole segments between `/` and `:`, the longest first: `target = "integrations/${aws_apigatewayv2_integration.orders.id}"`, `source_arn = "${aws_apigatewayv2_api.http.execution_arn}/*/*"`. Names, strings with spaces such as descriptions, and the IDs of resource groups and projects, which start every ID in them, stay as they are.
- **An Azure resource's `location`**, when it is its resource group's and the resource is in that group: `location = azurerm_resource_group.app.location`.
- **The Azure tenant**, as a Key Vault's `tenant_id = data.azurerm_client_config.current.tenant_id`, with the data source in `data.tf`.

A Google self link refers to its resource on either host Google's APIs use, as a private DNS zone's `network_url` names its network on `compute.googleapis.com`.

A value several resources have stays a literal, unless the argument names one's type or another argument of the same block refers to one (a Route 53 alias's `zone_id` follows its `name`). Tags and labels never become references, nor do region-wide settings whose ID is the region. The references are kept only if a new plan shows no extra changes. That is checked by resource: where a resource then plans differently, it keeps only the references of whole IDs and ARNs, or none, and the others keep theirs.

A resource the import listed but the selection left out, such as a default VPC or a default security group, is read through a data source instead: `security_groups = [aws_security_group.web.id, data.aws_security_group.sg_0abc1234.id]`, with the `data` blocks in `data.tf`. This only happens when the resource type has a data source that reads it by its ID, and the result is checked by plan in the same way. Resources another root of the same import manages are read the same way.

Where no data source can read what another root manages, such as a regional VPC, load balancer or KMS key in the global root, whose provider is in another region, or a role's ARN, the value becomes a variable in `variables.tf` that says where it comes from, with the value as its default: `role = var.app_role_arn`, with `description = "The ARN of aws_iam_role.app, which the root aws/123456789012/global manages. The default is its value when this root was imported."`. That covers IDs, ARNs (an IAM role's, after its path; a KMS key's), names where the argument names the type (`iam_instance_profile = var.ci_profile_name`), and a load balancer's DNS name or a bucket's regional domain name, as a CloudFront origin's `domain_name`. To keep roots in step, set the variables from the other root, for example from its outputs or state. A Google network, which is global, is read by a data source in the regional roots instead, whose subnetworks and routers name it by its self link: `network = data.google_compute_network.main.self_link`, with `name` and `project`.

What AWS manages in every account is read by name: the managed CloudFront cache, origin request and response headers policies (`cache_policy_id = data.aws_cloudfront_cache_policy.caching_disabled.id`, with `name = "Managed-CachingDisabled"`), and the AWS-managed EBS key, where an instance's, volume's or launch template's `kms_key_id` is a key no root manages (`data.aws_kms_alias.ebs.target_key_arn`, with `name = "alias/aws/ebs"`). The plan keeps a lookup only where it reads the same value.

Repeated values move into `locals.tf`:

- **Shared tags:** the tags most tagged resources in a directory share, key and value, become `local.tags`: those more than half of the resources with tags of their own have. Resources the import found with other tags, such as ones another team or tool manages, don't stop the move. On AWS they are applied through the provider's `default_tags`, and each resource keeps only its other tags and its own value of a shared tag, which wins over the provider's. AWS records every tag in `tags_all`, so `default_tags` must not tag a resource that doesn't have the tags, such as an untagged IAM user: those resources use a second configuration of the provider, `provider = aws.without_default_tags`, the same but for `default_tags`. Where those would be as many as the others, the resources that have the tags set `tags = local.tags`, or `merge(local.tags, { ... })` with their other tags, instead, and so they do if a plan shows that `default_tags` changes anything. On Google Cloud, labels shared the same way become `local.labels`, applied through the provider's `default_labels` (`google.without_default_labels` for the resources without them); `goog-` labels stay on the resources. On Azure, where azurerm has no such setting, each resource and module call that has the shared tags sets `tags = local.tags`, or `merge(local.tags, { ... })` with its other tags and its own values; `hidden-` tags stay on the resources. The move is kept only if a new plan shows no extra changes.
- **Repeated identifiers:** IDs and ARNs used three or more times (`vpc_id = "vpc-0abc1234"`) become locals named after the argument that uses them (`local.vpc_id`).

AWS resources don't repeat `region` (the provider's) or the computed `tags_all`, at the top or in nested blocks such as an instance's `root_block_device`. Google Cloud resources left in the root don't repeat the provider's `project` and `region`, where the provider gives them its own (curated modules take the project as an input), or `deletion_policy` at its default (`DELETE`, or `PREVENT` for a project), which is the provider's setting for `terraform destroy`, not the resource's.

Literal values have the type of the argument they set, as the provider's schema or the module's variable has it: `port = 80` for a number, `name = "80"` for a string, `enabled = true` for a bool.

Clusters of resources move into module calls: see [Modules](modules.md).

S3 buckets are imported split, as the AWS provider recommends: each part of a bucket's configuration it has (versioning, encryption, lifecycle, CORS, website, logging, public access block, ownership controls, transfer acceleration, requester pays, object lock, replication, policy) is its own resource, and the bucket's deprecated inline arguments are left out. ACLs aren't imported yet.

Security group rules are imported as `aws_vpc_security_group_ingress_rule` and `aws_vpc_security_group_egress_rule` resources of their own, by their `sgr-` IDs, as the AWS provider recommends; the groups leave out their inline `ingress` and `egress`.

Some configuration can't be read back from the cloud. A Lambda function deployed from a zip package gets `filename = "lambda/<function>.zip"` as a placeholder, which `lifecycle { ignore_changes = [filename, source_code_hash] }` tells Terraform to ignore: Terraform manages the function's settings, and the code stays deployed as it is. A comment in the block says how to have Terraform deploy the code instead. An import whose resource Terraform finds gone, such as a KMS key scheduled for deletion, is left out with that reason.

Generated local modules declare the attributes of their object variables `optional()`, so their calls set only the attributes that have values.

Last, the blocks are laid out as [HashiCorp's style guide](https://developer.hashicorp.com/terraform/language/style) has them. A resource's block starts with its meta-arguments (`count`, `for_each`, `provider`), then the arguments that identify it (`name`, and on Azure `resource_group_name` and `location`), the references to the resources it belongs to or uses (`vpc_id = aws_vpc.main.id`), its other arguments, and its `tags` or `labels`; then its nested blocks, `lifecycle` and `depends_on` last. One blank line separates the arguments from the nested blocks, in nested blocks too, and the nested blocks of a resource from each other. Blocks that hold nothing are written on one line, `allow {}`.

## Report

The `report/` directory of the output records the import:

| File | Contents |
|---|---|
| `coverage.json` | What the listers found, by type and directory, and what became of it: imported, left out (with Terraform's errors), not importable, or lost to a failed directory or service. Also the secret variables to set |
| `manifest.json` | The versions used: infraharvest, Terraform or OpenTofu, and the provider (constraint and locked version) |
| `report.md` | The same, for people |

With `--report-tags owner,team`, the report also counts what was discovered, imported and managed by the value of each tag key, including resources without the tag, so you can see which owners still have resources outside infrastructure as code. It counts the resources whose tags discover could read (see Choosing what to import). Reports hold no timestamps or absolute paths, so importing an unchanged estate produces the same files. With `--format json`, the whole report is also printed to stdout as one JSON document (`schema_version` 1); logs always go to stderr.

## Verification gate

After generating a directory, infraharvest runs its verification gate on it and records the results in the directory's README and in `coverage.json`:

| Check | Passes when |
|---|---|
| G1 format | `terraform fmt` has nothing to change |
| G2 validate | `terraform validate` passes |
| G3 plan | The plan imports every resource and changes nothing else. The secret variables of values the provider marks sensitive get placeholder values for this plan, so the arguments they set may change, and so may arguments the provider keeps only in state (for example a Secrets Manager secret's `recovery_window_in_days`, or a Google Cloud resource's `labels` and `terraform_labels`, which an import leaves empty). Those of the secrets infraharvest found get their values |
| G4 standards | The directory, and the local modules it calls, follow the output standard. Terraform and providers are pinned (`required_version` within one major release, providers with `~>`). Registry modules have an exact version, and git modules a `?ref=`. Variables and outputs are typed and described, and no sensitive variable has a default. There is no `"${...}"` around a single expression, and no argument the provider leaves out (such as an S3 bucket's deprecated inline settings). There are no state files and only `.tf` files. Local modules have `main.tf`, `variables.tf`, `outputs.tf`, `versions.tf` and a README, and no `examples/` |
| G5 scanners | The scanners installed on `PATH` run on the directory. tflint must find nothing at warning level or above, since its findings concern the generated code. trivy and checkov findings are listed but never fail the check: they describe the infrastructure's own settings, which the configuration must mirror to plan with no changes |
| G6 secrets | No written file contains a value the provider marks sensitive (unless another attribute holds the same value in the open, such as a parameter that holds its region), a secret value infraharvest found, any value [Secrets](#secrets) would take for a secret, a credential such as a private key, or an AWS access key ID with its secret access key (an ID alone, such as an IAM access key's, is an identifier) |
| G7 determinism | The configuration calls no function whose result changes between runs, such as `timestamp()` |

A failed check counts like a resource that couldn't be imported (exit code 1, or 3 with `--allow-partial`).

To check generated roots again, for example after editing them, run `infraharvest verify [output-directory]`. It runs the same checks on every root, initialising each without its backend, so it needs no access to the state; set secret variables first. `infraharvest report [output-directory]` prints an import's report again (`--format json` for JSON).

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Everything found was imported (types Terraform can't import aside) |
| 1 | Something couldn't be imported, and `--allow-partial` isn't set |
| 2 | The import couldn't run, for example without credentials or a Terraform binary |
| 3 | Something couldn't be imported, `--allow-partial` is set, and the output has the rest |

## Repairs

When the configuration Terraform generates doesn't validate, infraharvest fixes what Terraform rejects where that doesn't change its meaning, then plans again. It removes arguments that are unset in effect (zero values), arguments that duplicate another one (`subnets` next to `subnet_mapping` blocks), and nested blocks whose arguments are all null. Inside objects, it writes `null` for unset strings that Terraform generated as `""`.
