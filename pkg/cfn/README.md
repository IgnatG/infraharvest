# cfn: CloudFormation to Terraform

Package `cfn` translates AWS CloudFormation templates into Terraform configuration for the AWS provider. infraharvest uses it to carry a stack's template intent (its parameters, outputs, conditions and references) into the configuration it writes for the stack's live resources; it depends on nothing else of infraharvest's but its subpackage `hclname`, the names and numbers it shares with the rest, which depends on nothing, so it can be used on its own.

```go
t, err := cfn.Parse(file) // JSON or YAML, short-form tags included
if err != nil {
    return err
}
res, err := cfn.Convert(t, cfn.Options{})
if err != nil {
    return err
}
// res.Files: "main.tf", "variables.tf", "locals.tf", "outputs.tf", "data.tf"
// res.Diagnostics: what couldn't be carried over, and where
```

The output is formatted as `terraform fmt` would, and is the same, byte for byte, for the same template and options.

## What becomes what

| CloudFormation | Terraform |
|---|---|
| Parameter | `variable`, typed: `String` and AWS-specific types `string`, `Number` `number`, `List<Number>` `list(number)`, `CommaDelimitedList` and `List<AWS::...>` `list(string)` |
| `AllowedValues`, `AllowedPattern`, `MinLength`, `MaxLength`, `MinValue`, `MaxValue` | one `validation` block each, with `ConstraintDescription` as the message |
| `NoEcho` | `sensitive = true`; the default isn't written, so the secret stays out of the configuration |
| `AWS::SSM::Parameter::Value<...>` | a variable holding the parameter's name, and a `data "aws_ssm_parameter"` reading its value |
| Mappings | a map in `locals`; `Fn::FindInMap` indexes it, with `try()` for a `DefaultValue` |
| Conditions | boolean `locals` |
| Resource | `resource`, named by the Mapper; with a `Condition`, `count = local.<condition> ? 1 : 0`, and references index `[0]` |
| `DependsOn` | `depends_on` |
| `DeletionPolicy`, `UpdateReplacePolicy` | a comment saying what Terraform does instead; `Options.PreventDestroy` adds `prevent_destroy` for `Retain` |
| Output | `output`; with a `Condition`, null when the condition is false; sensitive when it reads a `NoEcho` parameter; an `Export` becomes a comment |
| `Ref` | `var.<parameter>`, or the resource attribute the Mapper names (usually `id`) |
| `Fn::GetAtt` | the attribute the Mapper names |
| `Fn::Sub` | a template string (a heredoc when it spans lines); `${!Literal}` stays literal |
| `Fn::Join`, `Fn::Select`, `Fn::Split`, `Fn::Base64` | `join()`, `element()`, `split()`, `base64encode()` |
| `Fn::Cidr` | `cidrsubnets()`, or `cidrsubnet()` in a `for` expression |
| `Fn::GetAZs` | `data.aws_availability_zones.available.names` |
| `Fn::If`, `Fn::Equals`, `Fn::And`, `Fn::Or`, `Fn::Not` | `? :`, `==`, `&&`, `\|\|`, `!` |
| `Fn::ImportValue` | a variable with a description: Terraform has no stack exports |
| `Fn::Length`, `Fn::ToJsonString` (AWS::LanguageExtensions) | `length()`, `jsonencode()` |
| `AWS::Region`, `AWS::AccountId`, `AWS::Partition`, `AWS::URLSuffix` | `data.aws_region`, `data.aws_caller_identity`, `data.aws_partition` |
| `AWS::StackName`, `AWS::StackId`, `AWS::NotificationARNs` | stand-in variables: Terraform has no stacks |
| `AWS::NoValue` | `null`; a property set to it is left out, a list element filtered out, and `Fn::If` of a block and `AWS::NoValue` a `dynamic` block |

## Naming: the Mapper

What a resource type, property and attribute are called in Terraform comes from a `Mapper`, which `Options.Mapper` replaces. A mapper of your own should embed `BaseMapper`, the built-in one, and define only the methods it answers better: what it leaves out, and any method `Mapper` gains later, then gets the built-in answer. `DefaultMapper` knows the common AWS types (VPC networking, S3, IAM, Lambda, SNS, SQS, KMS, CloudWatch Logs, DynamoDB, RDS, ECR, ECS, Route 53, load balancers, Secrets Manager, SSM parameters, EventBridge rules) and derives the rest: `AWS::Service::Thing` becomes `aws_service_thing` and a property its snake_case name. Derived names are marked as guesses, reported as diagnostics and listed in a comment above the resource, so that a person checks them.

A property can become an argument, a nested block (one per list element), a map with its keys as written, a tags map from `{Key, Value}` pairs, `jsonencode()` of a policy document, or nothing, with the reason (the S3 bucket's versioning, for example, is a separate resource since version 4 of the AWS provider).

## What isn't converted

Nothing is dropped silently: each of these is a `Diagnostic` (severity, path in the template, message) and a comment in the output that keeps what the template said.

- Macros and transforms (`Transform`, `Fn::Transform`, SAM's `AWS::Serverless` resources): CloudFormation expands them before it deploys the template, so convert the processed template (`aws cloudformation get-template --template-stage Processed`).
- `Fn::ForEach`.
- Custom resources (`Custom::*`, `AWS::CloudFormation::CustomResource`), nested stacks and wait conditions.
- Resource types outside the `AWS::` namespace, unless the Mapper knows them.
- `Rules`, `Hooks`, `Metadata` (including `AWS::CloudFormation::Init`), `CreationPolicy` and `UpdatePolicy`.
- `Fn::GetAtt` with a computed attribute name, and unknown functions: they become `null`.

## Limits

- `Fn::Equals` compares strings in CloudFormation. The converter writes a literal compared with a `Number` parameter as a number, and any other literal as a string; a comparison of two references keeps their types.
- `AllowedPattern` is checked with Terraform's `regex()`, which uses RE2: a pattern with look-ahead or back-references can't be, and is reported.
- Values are converted, not checked against the provider's schema: a value CloudFormation takes in another shape (a list of one for a single value, say) may need changing. The table covers the common cases, such as a security group rule's `CidrIp` becoming `cidr_blocks`.

## Untrusted templates

`Parse` and `Convert` are meant to take templates from anywhere, hostile ones included. Neither reads a file, the network or the environment, or runs anything a template names, and `FuzzParseConvert` checks that no template makes either panic or hang.

- `Parse` fails on a template larger than 16 MiB, one that nests values more than 512 deep, or one whose YAML aliases expand to more than 1M values or 64 MiB of text.
- `Convert` and `ConvertValue` fail once the expressions they write pass 64 MiB, so a template built to expand (nested `Fn::Sub` variables that repeat one another, say) can't exhaust memory.
- They check a `Template` built by hand, rather than by `Parse`, against limits of their own before they convert it: a nil entry in a section, or a value that contains itself (a `List` can), nests more than 1026 deep or holds more than 16M values counting each repeat, is an error rather than a stack overflow or an endless loop. `MarshalJSON` of a `Map` or `Func` fails the same way.
- The output is Terraform for a person to review before it is applied: it can still name any resource or value the template does. A `NoEcho` parameter's default is never written.
