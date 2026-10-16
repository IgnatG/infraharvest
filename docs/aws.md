# AWS

`infraharvest import aws` and `infraharvest discover aws` list the resources of one or more AWS accounts, region by region, and generate configuration for the `hashicorp/aws` provider. The [services reference](reference/services.md#aws) lists the services `--services` takes and the Terraform types each one lists.

```sh
infraharvest discover aws --profile prod --services=vpc,subnet,sg,s3 --regions=eu-west-2,eu-west-1
```

## Credentials

infraharvest signs in the way the AWS CLI and SDKs do. `--profile` names a profile of the shared configuration and credentials files (`~/.aws/config`, `~/.aws/credentials`), with whatever it uses: keys, an IAM Identity Center (SSO) session, a role to assume, or a `credential_process`. Left at `default`, the SDK's default chain decides, so `AWS_PROFILE`, the `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` variables, web identity (OIDC in CI) and the instance or container role all work, without a configuration file.

When a profile assumes a role that needs an MFA code, infraharvest asks for it on the terminal. Terraform gets the credentials infraharvest resolved, through `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_SESSION_TOKEN`, fresh for each root, so it never has to resolve an SSO session or ask for a code itself. Run `terraform plan` on the generated roots later with credentials of your own.

The account is the one `sts:GetCallerIdentity` returns. Throttled calls back off and retry, up to 10 attempts in the SDK's adaptive retry mode; `AWS_RETRY_MODE` and `AWS_MAX_ATTEMPTS` override that. `--rate-limit` caps the requests a second to each API and account. `--verbose` logs the headers of each request and response, never the bodies.

## Permissions

Read-only access is enough: the AWS-managed `ReadOnlyAccess` policy, with an explicit deny on reading data. The [`permissions/aws`](https://github.com/IgnatG/infraharvest/tree/main/permissions/aws) configuration creates a role with both; see [Permissions](permissions.md#aws).

## Regions

Without `--regions`, the import covers the region the profile or the environment sets (`AWS_REGION`, the profile's `region`). With it, each region is imported in turn into roots of its own: `<out>/aws/<account>/<region>/`.

Global services are imported once, not per region, into `<out>/aws/<account>/global/`, with the provider's region set to `us-east-1`: `budgets`, `cloudfront`, `ecrpublic`, `iam`, `organization`, `route53` and `waf`. `wafv2_cloudfront`, the web ACLs that CloudFront uses, exists only in `us-east-1` and is imported there. A service AWS doesn't offer in a region is skipped there, with a log message.

## Several accounts

`--accounts` imports the accounts it names, and `--organization` every active account of the organization the credentials belong to. Listing the organization's accounts needs `organizations:ListAccounts`, which the management account and a delegated administrator have. In each account, infraharvest assumes the role `--assume-role` names, where `{account}` stands for the account ID. It defaults to `arn:aws:iam::{account}:role/infraharvest-readonly`, the role the permissions configuration creates:

```sh
infraharvest import aws --organization --services=vpc,subnet,sg --regions=eu-west-2 --all \
  --assume-role 'arn:aws:iam::{account}:role/infraharvest-readonly' --parallel 4
```

Each account goes into roots of its own, so a custom `--layout` must contain `{account}`. `--parallel` imports that many accounts at once, each with its own Terraform. An account that fails, for example because the role can't be assumed, is reported, and the run goes on to the next. The generated provider block assumes the same role, from the credentials of whoever plans.

Without `--accounts` or `--organization`, an `--assume-role` ARN without `{account}` is assumed as it is, for the one account it names.

## Resource Explorer

Listing every service in every region makes many calls, most of them for services a region doesn't use. `--resource-explorer` asks [AWS Resource Explorer](https://docs.aws.amazon.com/resource-explorer/latest/userguide/welcome.html) first, and skips a service in a region when the index has none of the service's resource types there. It is off by default, because the index can take hours to find new resources, and one it hasn't found yet would be left out.

infraharvest only relies on the index when:

- the region has an active index, at least a day old;
- the region has a default view, or its index replicates to an aggregator index whose region has one;
- that view has no filter, and covers its own account only.

Otherwise it logs why and lists every service. Global services are always listed. So are services the index can't speak for: those with a type it doesn't support, such as launch configurations, those whose listers also find what other accounts share, such as subnets, VPCs and security groups, and those that list what AWS creates for the account. Types that can't exist without a parent, such as a load balancer's listeners, are looked up by their parent's type. When a region has more than 1,000 resources, infraharvest searches for each service's types on their own.

It needs `resource-explorer-2:GetIndex`, `GetDefaultView`, `GetView`, `ListSupportedResourceTypes` and `Search`, which `ReadOnlyAccess` grants. Turning Resource Explorer on in an account, with an aggregator index and a default view, is done once, in its console.

## What the default selection leaves out

`discover` leaves out what AWS creates and manages itself: default VPCs with their subnets, route tables and internet gateways, the main route table of every other VPC while it is as AWS created it (no tags, no routes but the local ones, no subnets of its own), default security groups and network ACLs, the security groups EKS creates, service-linked roles, the log groups Lambda creates, the default parameter, option and subnet groups of RDS and ElastiCache, and the AWS managed FullAWSAccess service control policy with its attachments. It leaves out the VPCs, subnets and transit gateways another account shares with this one through AWS RAM, naming the owner: that account manages them, and the configuration reads them through data sources. It also leaves out every resource a CloudFormation stack manages, nested stacks and CDK apps included, naming the stack: global resources are checked against the stacks of every enabled region. It leaves out the targets an Auto Scaling group or an ECS service registers in a target group, naming the group or service: they register and deregister them as they scale and deploy, and Terraform would register or deregister live targets on apply and destroy. It finds them with `autoscaling:DescribeAutoScalingGroups` and `ecs:ListClusters`, `ListServices` and `DescribeServices`, which `ReadOnlyAccess` grants, and only when the region has registered targets. Each is in the selection file with the reason, and `include: true` takes it in. See [Choosing what to import](usage.md#choosing-what-to-import). `eni` lists only the network interfaces people create, not those AWS services manage (Lambda's, load balancers', NAT gateways', VPC endpoints', ECS tasks'), nor an instance's primary one, which the instance owns.

## Generated configuration

Terraform writes every argument a resource has. infraharvest leaves out those at what AWS gives a resource that doesn't set them, where leaving them out changes no plan, so the configuration says what is particular to each resource. The catalogue lists them per type (see [Leaving out arguments](catalogue.md#leaving-out-arguments)), for example:

- an instance's zone when it has a subnet, the launch API's defaults (stop on shutdown, no detailed monitoring, default tenancy, the open capacity reservation preference, enclaves off, gp3's baseline IOPS and throughput), CPU options at the instance type's defaults or for a type that takes none (the lister compares them with `ec2:DescribeInstanceTypes`, which `ReadOnlyAccess` grants), and the credit option each burstable family launches with: `standard` for T2, `unlimited` for T3, T3a and T4g;
- a target group's stickiness when it is off, and its target group health at the thresholds AWS starts with; a listener's first action's order;
- a Lambda function's default architecture, ephemeral storage, tracing and logging to `/aws/lambda/<function>`, and its `code_sha256`, the hash of the code deployed;
- an on-demand DynamoDB table's capacity of 0, and point-in-time recovery, TTL and encryption with a key DynamoDB owns when they are off;
- a CloudFront cache behaviour's legacy `default_ttl` and `max_ttl` when it has a cache policy, and `minimum_protocol_version` with the default certificate;
- the default policies AWS writes: a KMS key's (`key-default-1`), an SNS topic's (`__default_policy_ID`), and a VPC endpoint's full access;
- the arguments only interface endpoints take, on a gateway endpoint;
- an ECS service's `iam_role` when its tasks use `awsvpc`, which a service with a network configuration runs, or when it is the service-linked role;
- an API Gateway v2 stage's `deployment_id` when the stage deploys itself;
- `arn`, which the provider computes, where it writes it back as an argument (SSM parameters, Kinesis streams).

A Lambda function whose code was deployed outside Terraform gets a placeholder `filename` and ignores changes to it and to `code_sha256`, which in provider 6 is what makes the provider update the code: to deploy it with Terraform, put the package at `filename`, set `code_sha256 = filebase64sha256(...)` of it, and remove the `ignore_changes`.

An API Gateway deployment gets `create_before_destroy`, as the provider's documentation recommends, since a stage refers to it. It doesn't get the `triggers` the documentation also recommends: AWS doesn't keep them, so set on an imported deployment, they would replace it on the first plan. A comment says how to add them, at the cost of that one replacement.

A load balancer listener that forwards to one target group keeps both `target_group_arn` and the `forward` block the import reads: the provider can't tell which of the two was set, and the configuration without either plans an update.

## Services at end of life

Cloud9 (`cloud9`) and Data Pipeline (`datapipeline`) are closed to new customers; MediaStore (`media_store`) and WAF Classic (`waf`, `waf_regional`) have reached end of support. What still exists in them can be imported. `cloud9` lists only EC2 environments, as `aws_cloud9_environment_ec2`: an SSH environment has no Terraform type, so it is skipped with a log message. `media_store` leaves out containers being deleted; the provider has deprecated `aws_media_store_container` and will remove it in a future version. `waf_regional` also lists which load balancers and API Gateway stages each web ACL protects, as `aws_wafregional_web_acl_association`.

## Tags

Tags come from the Resource Groups Tagging API of each region listed, and of `us-east-1` for the global services, for the selection file's rules, the picker and the report. That API lists tags by ARN, so resources whose ID isn't an ARN are matched through the ARN of their type where the catalogue knows it; IAM roles and users have no tags there. In the generated configuration, the tags all resources of a root share move to the provider's `default_tags`; `aws:` tags stay on the resources.

## Filtering

`--id` applies to every service, by the type without `aws_`:

```sh
infraharvest import aws --services=vpc,subnet --regions=eu-west-2 --all --id=vpc=vpc-0a1b2c3d
infraharvest import aws --services=s3 --regions=eu-west-2 --all --id=s3_bucket=my-logs,my-assets
```

`--tag` works for three services: EC2 instances (`ec2_instance`), EBS volumes (`ebs`) and API Gateway REST APIs (`api_gateway`). The first two pass it to the EC2 API; the third checks each REST API's tags. It applies to all three:

```sh
infraharvest import aws --services=ec2_instance,ebs --regions=eu-west-2 --all \
  --tag Environment=prod
```

See [Filtering](usage.md#filtering) for the syntax.

`SQS_PREFIX`, when set, makes the `sqs` service list only the queues whose names start with it.

## Services listed through Cloud Control

Some services have no lister of their own: they list their types through the [Cloud Control API](https://docs.aws.amazon.com/cloudcontrolapi/latest/userguide/what-is-cloudcontrolapi.html), which lists the resources of any CloudFormation type. They are the catalogue's types with a `cloud_control` entry, grouped by service, such as `eventbridge`, `backup` and `guardduty` (see [Catalogue](catalogue.md)). Adding a type that way takes a catalogue entry, not code.
