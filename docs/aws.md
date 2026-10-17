# AWS

`infraharvest import aws` and `infraharvest discover aws` list the resources of one or more AWS accounts, region by region, and generate configuration for the `hashicorp/aws` provider. The [services reference](reference/services.md#aws) lists the services `--services` takes and the Terraform types each one lists.

```sh
infraharvest discover aws --profile prod --services=vpc,subnet,sg,s3 --regions=eu-west-2,eu-west-1
```

## Credentials

infraharvest signs in the way the AWS CLI and SDKs do. `--profile` names a profile of the shared configuration and credentials files (`~/.aws/config`, `~/.aws/credentials`), with whatever it uses: keys, an IAM Identity Center (SSO) session, a role to assume, or a `credential_process`. Left at `default`, the SDK's default chain decides, so `AWS_PROFILE`, the `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` variables, web identity (OIDC in CI) and the instance or container role all work, without a configuration file.

When a profile assumes a role that needs an MFA code, infraharvest asks for it on the terminal, once for every region of the import: the regions of one profile and role share the credentials, and the role is assumed once. Terraform gets the credentials infraharvest resolved, through `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and `AWS_SESSION_TOKEN`, so it never has to resolve an SSO session or ask for a code itself. They are given again before each plan of a root, and temporary credentials are refreshed five minutes before they expire, so a root that plans for longer than a session lasts doesn't fail partway with expired ones; a role that needs MFA asks for a new code then. Run `terraform plan` on the generated roots later with credentials of your own.

The account is the one `sts:GetCallerIdentity` returns, asked once for each set of credentials. Each root's provider block names its region, the default one the import resolved when `--regions` isn't set, and the account in `allowed_account_ids`, so that a later plan with other credentials or another default region fails rather than reading other resources. Throttled calls back off and retry, up to 10 attempts in the SDK's adaptive retry mode; `AWS_RETRY_MODE` and `AWS_MAX_ATTEMPTS` override that. `--rate-limit` caps the requests a second to each API and account. `--verbose` logs the headers of each request and response, never the bodies, with the signature, session token and cookies redacted.

## Permissions

Read-only access is enough: the AWS-managed `ReadOnlyAccess` policy, with an explicit deny on reading data. The [`permissions/aws`](https://github.com/IgnatG/infraharvest/tree/main/permissions/aws) configuration creates a role with both; see [Permissions](permissions.md#aws).

## Regions

Without `--regions`, the import covers the region the profile or the environment sets (`AWS_REGION`, the profile's `region`). With it, each region is imported in turn into roots of its own: `<out>/aws/<account>/<region>/`.

Global services are imported once, not per region, into `<out>/aws/<account>/global/`, with the provider's region set to `us-east-1`: `budgets`, `cloudfront`, `ecrpublic`, `iam`, `organization`, `route53` and `waf`. `wafv2_cloudfront`, the web ACLs that CloudFront uses, exists only in `us-east-1` and is imported there, with the regional services when `--regions` includes `us-east-1`. So is `dx_gateway`, the Direct Connect gateways: they are global, and every region answers with all of them, so they are imported once rather than into every region's root; `dx` lists each region's virtual interfaces and connections. A custom `--layout` without `{region}` still gets `global/`, `us-east-1/` and each region's directory added when a run would otherwise put several of them in one root. A service AWS doesn't offer in a region is skipped there, with a log message.

`route53` lists each hosted zone with its records, then the health checks. When the records of a zone, or the health checks, fail to list, what was listed is imported all the same and the service is reported as failed, so the import exits 1, or 3 with `--allow-partial`; when the zones themselves fail to list, nothing of `route53` is imported. The other services that read parts of what they list one by one do the same when a part fails, other than for being deleted since it was listed: `alb` (a load balancer's listeners, a listener's rules or certificates, a target group's targets), `ecs` (a cluster's services), `efs` (a file system's mount targets or policy), `kms` (a key that can't be described, a key's grants), `ecr` (a repository's policies), `s3` (a bucket's location or policy) and `medialive` (one of its three lists).

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

`discover` leaves out what AWS creates and manages itself: default VPCs with their subnets, route tables and internet gateways, the main route table of every other VPC while it is as AWS created it (no tags, no routes but the local ones, no subnets of its own), default security groups and network ACLs, the security groups EKS creates, the Auto Scaling groups and launch templates EKS creates for a managed node group (tagged `eks:nodegroup-name`), which the node group manages, service-linked roles, the log groups Lambda creates, the metric alarms a target tracking scaling policy creates (`TargetTracking-...`), the ACM certificates that failed, timed out validating, expired or were revoked, the default parameter, option and subnet groups of RDS and ElastiCache, and the AWS managed FullAWSAccess service control policy with its attachments. It leaves out the KMS grants AWS services such as RDS and EBS make for themselves, to a service principal or a service-linked role, naming the grantee: every argument of a grant forces a new one, and replacing it could break decryption. It leaves out the VPCs, subnets and transit gateways another account shares with this one through AWS RAM, naming the owner: that account manages them, and the configuration reads them through data sources. It imports a DynamoDB global table (version 2019.11.21) once, as an `aws_dynamodb_table` with a `replica` block for each other region, and leaves out its replicas in those regions, naming the region that imports it ("a replica of the global table in eu-west-1"): AWS doesn't say in which region a global table was created, so that region is the first by name of the table's regions, which every region agrees on. The `dynamodb` lister reads the replicas with `dynamodb:DescribeTable`, which `ReadOnlyAccess` grants, once per table. Tables of the global tables of 2017 are separate tables in each region, and each region imports its own. It also leaves out every resource a CloudFormation stack manages, nested stacks and CDK apps included, naming the stack: global resources are checked against the stacks of every enabled region. A stack's resource is matched by its physical ID and, where infraharvest knows which Terraform types its CloudFormation type can be listed as, by type as well, so a hand-made IAM role named as a stack's instance profile or table is still imported. The types are in [`cloudformation_types.go`](https://github.com/IgnatG/infraharvest/blob/main/internal/providers/aws/cloudformation_types.go), together with the CloudFormation type of each catalogue entry with `cloud_control`. A Terraform type covers the types that hold parts of it under the same ID, such as `aws_s3_bucket_policy` for `aws_s3_bucket`. A CloudFormation type it doesn't know, such as a custom resource, leaves out whatever has its physical ID, so that nothing a stack manages is imported. It leaves out the stacks Elastic Beanstalk (`awseb-e-...`), a stack set (`StackSet-...`) and Service Catalog (`SC-...`) deploy and manage, told by their names. It leaves out the targets an Auto Scaling group, an ECS service or the AWS Load Balancer Controller of an EKS cluster registers in a target group, naming the group, service or cluster: they register and deregister them as they scale and deploy, and Terraform would register or deregister live targets on apply and destroy. The controller's target groups are told by the `elbv2.k8s.aws/cluster` tag it gives those it creates (a `TargetGroupBinding` to a target group made elsewhere has none). It finds the others with `autoscaling:DescribeAutoScalingGroups` and `ecs:ListClusters`, `ListServices` and `DescribeServices`, which `ReadOnlyAccess` grants, and only when the region has registered targets. Each is in the selection file with the reason, and `include: true` takes it in. See [Choosing what to import](usage.md#choosing-what-to-import). `eni` lists only the network interfaces people create, not those AWS services manage (Lambda's, load balancers', NAT gateways', VPC endpoints', ECS tasks'), nor an instance's primary one, which the instance owns, nor those the Amazon VPC CNI plugin and the VPC resource controller of an EKS cluster attach to its nodes (told by their `node.k8s.amazonaws.com/instance_id` or `cluster.k8s.amazonaws.com/name` tag, or an `aws-K8S-` description), which they create and delete as pods come and go. `vpc_endpoint` lists only the endpoints the account manages, not those an AWS service creates for itself (GuardDuty runtime monitoring's, for one), nor those deleting, deleted, rejected, failed or expired.

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

Of each Lambda layer, `lambda` imports the latest version and the older versions a listed function uses, not the history of every version published: they all come from the lists of layers and functions, with no call per layer. A layer version gets a placeholder `filename` too, and ignores changes to it and to `source_code_hash`.

A KMS key is generated as the type Terraform has for it: `aws_kms_key` for a key whose material KMS made, `aws_kms_external_key` for one whose material was imported, and `aws_kms_replica_key` or `aws_kms_replica_external_key` for a multi-Region replica, which `aws_kms_key` refuses to read. The lister tells them apart from `kms:DescribeKey`, which it calls for every key already.

An API Gateway deployment gets `create_before_destroy`, as the provider's documentation recommends, since a stage refers to it. It doesn't get the `triggers` the documentation also recommends: AWS doesn't keep them, so set on an imported deployment, they would replace it on the first plan. A comment says how to add them, at the cost of that one replacement.

A load balancer listener that forwards to one target group keeps both `target_group_arn` and the `forward` block the import reads: the provider can't tell which of the two was set, and the configuration without either plans an update.

## Services at end of life

Cloud9 (`cloud9`) and Data Pipeline (`datapipeline`) are closed to new customers; MediaStore (`media_store`) and WAF Classic (`waf`, `waf_regional`) have reached end of support. What still exists in them can be imported. `media_store`, `waf` and `waf_regional` are opt-in: `--services='*'` leaves them out, so that every import doesn't ask their retired APIs in every region, and they are listed only when `--services` names them. `cloud9` lists only EC2 environments, as `aws_cloud9_environment_ec2`: an SSH environment has no Terraform type, so it is skipped with a log message. `media_store` leaves out containers being deleted; the provider has deprecated `aws_media_store_container` and will remove it in a future version. `waf_regional` also lists which load balancers and API Gateway stages each web ACL protects, as `aws_wafregional_web_acl_association`.

## Tags

Tags come from the Resource Groups Tagging API of each region listed, and of `us-east-1` for the global services, for the selection file's rules, the picker and the report. That API lists tags by ARN, so resources whose ID isn't an ARN are matched through the ARN of their type where the catalogue knows it; IAM roles and users have no tags there. Only the services of the listed resources are asked for, so a run of a few services doesn't read every tagged resource of the region. In the generated configuration, the tags all resources of a root share move to the provider's `default_tags`; `aws:` tags stay on the resources.

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
