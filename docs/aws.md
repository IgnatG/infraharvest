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

Without `--regions`, the import covers the region the profile or the environment sets (`AWS_REGION`, the profile's `region`). With it, each region is imported into roots of its own: `<out>/aws/<account>/<region>/`. Every region of the account, and its global services, are listed before any root is generated, so that the roots refer to each other's resources (see [Tidying](output.md#tidying)). The global services and those only in us-east-1 are listed and generated first, one at a time; then `--parallel` regions go at once, each with its own Terraform, and the report keeps the order of `--regions` whichever finishes first. The log says when each region starts and finishes listing (`aws: listed resources in region eu-west-1`) and generating its roots (`aws: generated the roots in region eu-west-1`). A region that fails stops the others of its account, and nothing of the account is generated if one fails to list.

Global services are imported once, not per region, into `<out>/aws/<account>/global/`, with the provider's region set to `us-east-1`: `budgets`, `cloudfront`, `ecrpublic`, `iam`, `organization`, `route53` and `waf`. `wafv2_cloudfront`, the web ACLs that CloudFront uses, exists only in `us-east-1` and is imported there, with the regional services when `--regions` includes `us-east-1`. So is `dx_gateway`, the Direct Connect gateways: they are global, and every region answers with all of them, so they are imported once rather than into every region's root; `dx` lists each region's virtual interfaces and connections. A custom `--layout` without `{region}` still gets `global/`, `us-east-1/` and each region's directory added when a run would otherwise put several of them in one root. A service AWS doesn't offer in a region is skipped there, with a log message.

`route53` lists each hosted zone with its records, then the health checks. When the records of a zone, or the health checks, fail to list, what was listed is imported all the same and the service is reported as failed, so the import exits 1, or 3 with `--allow-partial`; when the zones themselves fail to list, nothing of `route53` is imported. The other services that read parts of what they list one by one do the same when a part fails, other than for being deleted since it was listed: `alb` (a load balancer's listeners, a listener's rules or certificates, a target group's targets), `ecs` (a cluster's services), `efs` (a file system's mount targets or policy), `kms` (a key that can't be described, a key's grants), `ecr` (a repository's policies), `s3` (a bucket's location or policy) and `medialive` (one of its three lists).

## Several accounts

`--accounts` imports the accounts it names, and `--organization` every active account of the organization the credentials belong to. Listing the organization's accounts needs `organizations:ListAccounts`, which the management account and a delegated administrator have. In each account, infraharvest assumes the role `--assume-role` names, where `{account}` stands for the account ID. It defaults to `arn:aws:iam::{account}:role/infraharvest-readonly`, the role the permissions configuration creates:

```sh
infraharvest import aws --organization --services=vpc,subnet,sg --regions=eu-west-2 --all \
  --assume-role 'arn:aws:iam::{account}:role/infraharvest-readonly' --parallel 4
```

Each account goes into roots of its own, so a custom `--layout` must contain `{account}`. `--parallel` imports that many accounts at once, each with its own Terraform; their regions count towards the same limit, so no more than `--parallel` regions run at once in all (see [Running in parallel](usage.md#running-in-parallel)). An account that fails, for example because the role can't be assumed, is reported, and the run goes on to the next. The generated provider block assumes the same role, from the credentials of whoever plans.

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

`discover` leaves out what AWS creates and manages itself: default VPCs with their subnets, route tables and internet gateways, the main route table of every other VPC while it is as AWS created it (no tags, no routes but the local ones, no subnets of its own), default security groups and network ACLs, the security groups EKS creates, the Auto Scaling groups and launch templates EKS creates for a managed node group (tagged `eks:nodegroup-name`), which the node group manages, service-linked roles, the log groups Lambda creates, the metric alarms a target tracking scaling policy creates (`TargetTracking-...`), the ACM certificates that failed, timed out validating, expired or were revoked, the default parameter, option and subnet groups of RDS and ElastiCache, and the AWS managed FullAWSAccess service control policy with its attachments. It leaves out IAM access keys, long-lived credentials whose secret the import can't read: `discover` lists them for the selection file, and an import lists them, one `iam:ListAccessKeys` call per user, only when the selection file includes some. It tells the default network apart from what the `vpc`, `subnet`, `route_table`, `igw` and `sg` listers record: whether a VPC is the default one, the VPC of each subnet, route table and internet gateway, and the name of each security group (AWS reserves `default` for the group it creates with a VPC). It asks EC2 only for what the listing leaves open, such as the default VPCs when subnets are listed without the `vpc` service. It leaves out the KMS grants AWS services such as RDS and EBS make for themselves, to a service principal or a service-linked role, naming the grantee: every argument of a grant forces a new one, and replacing it could break decryption. It leaves out the VPCs, subnets and transit gateways another account shares with this one through AWS RAM, naming the owner: that account manages them, and the configuration reads them through data sources. It imports a DynamoDB global table (version 2019.11.21) once, as an `aws_dynamodb_table` with a `replica` block for each other region, and leaves out its replicas in those regions, naming the region that imports it ("a replica of the global table in eu-west-1"): AWS doesn't say in which region a global table was created, so that region is the first by name of the table's regions, which every region agrees on. The `dynamodb` lister reads the replicas with `dynamodb:DescribeTable`, which `ReadOnlyAccess` grants, once per table. Tables of the global tables of 2017 are separate tables in each region, and each region imports its own. It also leaves out every resource a CloudFormation stack manages, nested stacks and CDK apps included, naming the stack: global resources are checked against the stacks of every enabled region. A stack's resource is matched by its physical ID and, where infraharvest knows which Terraform types its CloudFormation type can be listed as, by type as well, so a hand-made IAM role named as a stack's instance profile or table is still imported. The types are in [`cloudformation_types.go`](https://github.com/IgnatG/infraharvest/blob/main/internal/providers/aws/cloudformation_types.go), together with the CloudFormation type of each catalogue entry with `cloud_control`. A Terraform type covers the types that hold parts of it under the same ID, such as `aws_s3_bucket_policy` for `aws_s3_bucket`. A CloudFormation type it doesn't know, such as a custom resource, leaves out whatever has its physical ID, so that nothing a stack manages is imported. It leaves out the stacks Elastic Beanstalk (`awseb-e-...`), a stack set (`StackSet-...`) and Service Catalog (`SC-...`) deploy and manage, told by their names. It leaves out the targets an Auto Scaling group, an ECS service or the AWS Load Balancer Controller of an EKS cluster registers in a target group, naming the group, service or cluster: they register and deregister them as they scale and deploy, and Terraform would register or deregister live targets on apply and destroy. The controller's target groups are told by the `elbv2.k8s.aws/cluster` tag it gives those it creates (a `TargetGroupBinding` to a target group made elsewhere has none). It reads the others from the Auto Scaling groups and ECS services the `auto_scaling` and `ecs` listers listed, and asks `autoscaling:DescribeAutoScalingGroups` and `ecs:ListClusters`, `ListServices` and `DescribeServices`, which `ReadOnlyAccess` grants, only when the region has registered targets of a target group none of those claims. Each is in the selection file with the reason, and `include: true` takes it in. See [Choosing what to import](usage.md#choosing-what-to-import). `eni` lists only the network interfaces people create, not those AWS services manage (Lambda's, load balancers', NAT gateways', VPC endpoints', ECS tasks'), nor an instance's primary one, which the instance owns, nor those the Amazon VPC CNI plugin and the VPC resource controller of an EKS cluster attach to its nodes (told by their `node.k8s.amazonaws.com/instance_id` or `cluster.k8s.amazonaws.com/name` tag, or an `aws-K8S-` description), which they create and delete as pods come and go. `vpc_endpoint` lists only the endpoints the account manages, not those an AWS service creates for itself (GuardDuty runtime monitoring's, for one), nor those deleting, deleted, rejected, failed or expired.

## Migrating a CloudFormation stack

`--cfn-stack` turns the default selection around: instead of leaving out what CloudFormation stacks manage, the import takes the resources of the stacks it names, and of the stacks nested in them, recursively, and nothing else. Name a live stack by name, or any stack by its stack ID (its ARN), which also finds a stack deleted in the last 90 days; repeat the flag for other stacks. A stack named by name is looked for in the one region of `--regions`, or the profile's. `--cfn-stack` says what to import, so it needs neither `--all` nor a selection file, though it takes one, which then decides among the stacks' resources only. It reaches the account the credentials reach, or the one an `--assume-role` ARN names, not `--accounts` or `--organization`.

```sh
infraharvest discover aws --profile prod --cfn-stack app
infraharvest import aws --profile prod --cfn-stack app --cfn-stack arn:aws:cloudformation:eu-west-2:123456789012:stack/old/1a2b3c4d
```

Before anything is listed, infraharvest reads each stack, changing nothing: `cloudformation:DescribeStacks` once for each stack named, then `ListStackResources` and `GetTemplate` (the template after its transforms, the `Processed` stage) once for each stack, nested ones included, which `ReadOnlyAccess` grants. It imports in the stacks' regions, with the global services, such as IAM, in their own call as `--regions` would, and without `--services` lists only the services of the stacks' resource types. It doesn't scan the region's other stacks, as the default selection does. Stack resources are matched with what is listed by their physical IDs and types, as for [what the default selection leaves out](#what-the-default-selection-leaves-out), from the listed data alone: a resource that shares its ID with another, such as a bucket policy, which has its bucket's, goes to the stack's resource of its type whose properties name the other, though its physical ID differs. Everything else listed is excluded, as outside the import's scope, and no selection file rule brings it back. The log says, for each stack, how many resources it has, which can't be imported and which aren't retained.

Some resources can't be imported, and the report says why: custom resources (`Custom::*`, `AWS::CloudFormation::CustomResource`), which CloudFormation creates through a Lambda function or an SNS topic (see [Custom resources](#custom-resources)), and types infraharvest knows no Terraform type for. Each keeps the stack from being safe to delete until `--cfn-ignore` acknowledges it, by its logical ID, or as `<stack>/<logical ID>` where several stacks have it; the report shows it as acknowledged. Acknowledging accepts only that Terraform doesn't import it: the resource still needs `Retain`, which the copy of the template with `Retain` added gives it, so that deleting the stack never calls its handler, and the stack isn't safe to delete without it. A nested stack (`AWS::CloudFormation::Stack`) isn't imported as an `aws_cloudformation_stack`: its resources are imported with the rest. Wait conditions and the CDK's `AWS::CDK::Metadata` exist only in CloudFormation, so there is nothing to import. infraharvest warns about a stack Elastic Beanstalk, a stack set or Service Catalog deploys, as that service may change or create again what it holds, and about a nested stack named on its own, which its parent deletes with itself. Of a deleted stack, it imports what the stack retained (`DELETE_SKIPPED`).

For a stack to be deleted without destroying what is now in Terraform, every resource needs `DeletionPolicy: Retain` (or `RetainExceptOnCreate`) and `UpdateReplacePolicy: Retain`. The report says, resource by resource, what each lacks, CloudFormation's defaults included: `Delete` for most types, and `Snapshot` for an `AWS::RDS::DBCluster` and for an `AWS::RDS::DBInstance` outside a cluster, which deletes it after a final snapshot. `Snapshot`, which `AWS::DocDB::DBCluster`, `AWS::EC2::Volume`, `AWS::ElastiCache::CacheCluster`, `AWS::ElastiCache::ReplicationGroup`, `AWS::Neptune::DBCluster`, `AWS::Redshift::Cluster` and the RDS types take, keeps their data, not them. A policy a function sets is reported as not evaluated. infraharvest writes each stack's template to `<out>/.infraharvest/cloudformation/<region>/<stack>.template.json` (or `.yaml`, as the stack has it) and, where a resource lacks them, a copy with both policies set to `Retain`, `<stack>.retain.template.json`: `.infraharvest` is git-ignored and only you can read it, as a template may hold secret values. Nested stacks, and what exists only in CloudFormation, keep their policies, so that deleting the root stack deletes the nested stacks too. The copy is of the processed template, without the `Transform` the stack was created with; if the template is under source control, add `Retain` there instead.

The [report](output.md#report) has a section for each stack (`cloudformation_stacks` in its JSON), with every resource's logical ID, type, physical ID, what became of it, the addresses it was imported at, and whether it is retained. It says a stack is safe to delete only when every resource was imported, or left out by the selection file or already in a `--managed-state` state, the gate of every root that imports some passed, so their plans change nothing, and every resource is retained; otherwise it lists what keeps it from being so. Deleting a stack deletes those nested in it, so a stack is safe to delete only when they are too, and their blockers are its own. The steps, which infraharvest leaves to you:

1. Update the stack with the copy that has `Retain` added, `aws cloudformation update-stack --stack-name app --template-body file://<out>/.infraharvest/cloudformation/eu-west-2/app.retain.template.json`, with the capabilities and parameters the stack was created with, once a change set shows it changes only the policies. A nested stack's copy goes to S3, and its parent's `TemplateURL` names it.
2. Apply the generated roots, which import the resources and change nothing else.
3. Delete the stack: CloudFormation retains every resource, Terraform's now.

What the report can't see, it can't vouch for: drift between the stack and its resources, and a stack policy or termination protection that would stop the update or the deletion. Check the change set of step 1, and run `aws cloudformation detect-stack-drift` first if the stack may have drifted.

### Custom resources

A custom resource's handler runs when CloudFormation creates, updates or deletes it, so deleting the stack can undo what it did, unless the resource is retained. infraharvest knows the custom resources the AWS CDK's own constructs create, and the report says, for each, what it does and what to do, naming what it acts on from the template:

| Type | CDK construct | What to do |
|---|---|---|
| `Custom::S3AutoDeleteObjects` | a bucket's `autoDeleteObjects` | Deleting it empties the bucket, retained or not. Take it out of the stack first (see below); `--cfn-ignore` doesn't acknowledge it |
| `Custom::ECRAutoDeleteImages` | a repository's `emptyOnDelete` | Deleting it deletes the repository's images. Take it out of the stack first, and remove the repository's `aws-cdk:auto-delete-images` tag |
| `Custom::SyntheticsAutoDeleteUnderlyingResources` | a canary's `cleanup` | Deleting it deletes the canary's Lambda function and layers. Take it out of the stack first |
| `Custom::LogRetention` | a function's `logRetention` | Import the log group, with its retention |
| `Custom::CDKBucketDeployment` | `BucketDeployment` | Deploy the files another way: Terraform imports the bucket, not them |
| `Custom::AWS` | `AwsCustomResource` | Its `onDelete` call runs when it is deleted: manage what its calls set up in Terraform |
| `Custom::VpcRestrictDefaultSG` | a VPC's `restrictDefaultSecurityGroup` | Deleting it restores the default group's rules: manage them with `aws_default_security_group` |
| `Custom::AWSCDKOpenIdConnectProvider` | `iam.OpenIdConnectProvider` | Import the provider as `aws_iam_openid_connect_provider` by its ARN, the physical ID |
| `Custom::S3BucketNotifications` | a bucket's notifications | Deleting it clears them: import the bucket's `aws_s3_bucket_notification` |
| `Custom::CrossRegionExportWriter`, `Custom::CrossRegionExportReader` | `crossRegionReferences` | Replace the SSM parameters it writes with Terraform's own references |
| `Custom::CrossAccountZoneDelegation` | `CrossAccountZoneDelegationRecord` | Import the NS record into the parent account's Terraform |
| `Custom::DeleteExistingRecordSet`, `Custom::Trigger`, `Custom::AWSCDK-EKS-KubernetesObjectValue` | a record's `deleteExisting`, `Trigger`, a Kubernetes object's value | Nothing is left to manage |
| `Custom::AWSCDK-EKS-FargateProfile`, `Custom::AWSCDK-EKS-KubernetesResource`, `Custom::AWSCDK-EKS-KubernetesPatch`, `Custom::AWSCDK-EKS-HelmChart` | an `eks.Cluster`'s Fargate profiles, manifests, patches and charts | Manage them with `aws_eks_fargate_profile`, or the `kubernetes` and `helm` providers |

`eks.Cluster` creates its cluster through `Custom::AWSCDK-EKS-Cluster`, whose physical ID is the cluster's name: infraharvest imports it as `aws_eks_cluster`, as it does an `AWS::EKS::Cluster`. Any other custom resource gets the general advice. Every one needs `Retain`, like the stack's other resources, so that deleting the stack doesn't call its handler, and, once dealt with, `--cfn-ignore` to acknowledge it, but for the three that destroy what the stack's resources hold. Those must be gone from the stack before it is deleted: update the stack with the copy of its template that retains them, so that CloudFormation no longer calls their handlers; then take them out, with `autoDeleteObjects: false` (`emptyOnDelete: false`, `cleanup: Cleanup.NOTHING`) in the CDK app, or by removing them from the template, and update the stack again; and remove the bucket's `aws-cdk:auto-delete-objects` tag, or the repository's `aws-cdk:auto-delete-images` tag, which their handlers act on. The next import no longer finds them.

### The template in the roots

The roots hold each resource as it is live, which stays the source of truth, and the stack's template adds what the live configuration can't say: which values were parameters, and what the stack output. Once a root's files are final, before its [gate](output.md#report) runs, infraharvest carries the template of each stack into the root, in `cloudformation_<stack>.tf`, with only edits that can't change a value, which the gate then checks with the rest, so the plan still changes nothing:

- A literal becomes `var.<parameter>` where the template sets that very argument to `Ref` of the parameter, the argument is one infraharvest knows the property maps to (from its CloudFormation types and the provider's schema, not a guess), and the literal equals the parameter's value in the stack, as `DescribeStacks` gives it, as the same kind of value. The variable is typed from the parameter's type, its default is that value, and `AllowedValues` becomes a `validation` block.
- An output becomes an `output` when its whole value converts: each resource it refers to is in that root, each attribute is one the provider's schema has, and each parameter it reads has a variable, declared for it. `AWS::StackName` and `AWS::StackId` become the stack's name and ID, `AWS::Region`, `AWS::AccountId` and `AWS::Partition` data sources. An `Export` becomes a comment: another configuration reads the output from the root's state instead.
- The conditions are comments: the root holds what was deployed, whatever they said.

What can't be applied that way is reported, in the root's warnings in the report and in comments in the file, and left as it is: a parameter used inside `Fn::Sub` or `Fn::Join`, a `NoEcho` parameter, whose value CloudFormation doesn't give (the secret attributes it sets are sensitive variables already), a property whose live value differs from its parameter's, which is drift, and an output with a condition, one that refers to a resource of another root or to nothing of the root's. A variable is declared only when something refers to it. The parameters' values go into the variables' defaults, as a template's parameters aren't secret unless they are `NoEcho`; they aren't written into the report. A nested stack's parameters aren't read, so its template contributes its outputs only. `--incremental`, which adds to a root rather than generating it, doesn't carry the template into it.

### Translating a template on its own

`infraharvest cfn convert <template>` translates a template, JSON or YAML (`-` reads standard input), into Terraform files with no cloud account: its intent rather than live resources, for a stack not deployed yet or as a starting point. It writes `variables.tf` from the parameters, with their constraints as `validation` blocks, `locals.tf` from the mappings and conditions, `main.tf` from the resources, with `count` for a conditional one, `outputs.tf` and `data.tf` into `--out` (default `cfn-terraform`), and refuses to overwrite files there.

```sh
infraharvest cfn convert app.template.yaml --out app-terraform --stack-name app
```

Intrinsic functions become the expressions that compute the same values: `Ref` and `Fn::GetAtt` references, `Fn::Sub` template strings, `Fn::Join` `join()`, `Fn::If` conditionals, and so on. What has no Terraform equivalent (custom resources, nested stacks, macros and transforms such as `AWS::Serverless`, `Fn::ForEach`, unknown functions) is a diagnostic on standard error, and a comment in the files where it would have been; it exits 1 when something couldn't be converted (3 with `--allow-partial`). Names come from a built-in table of common types and are otherwise derived from the CloudFormation names, and each derived name is reported, so check them against the provider's documentation. The translator is the Go package `pkg/cfn` of this repository, which has no dependency on the rest of infraharvest.

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
