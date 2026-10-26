# ---- main.tf ----

# error: the template uses the AWS::Serverless-2016-10-31 transform, which CloudFormation expands into other resources before it deploys them: convert the processed template instead (aws cloudformation get-template --template-stage Processed)
# error: the template uses the macro AcmeMacro, which CloudFormation runs on the template before it deploys it: convert the processed template instead (aws cloudformation get-template --template-stage Processed)
# error: Fn::ForEach isn't converted: it repeats an entry when CloudFormation processes the template. Convert the processed template, or write the entry once with for_each
# It was: ["TopicName",["Alerts","Events"],{"Topic${TopicName}":{"Type":"AWS::SNS::Topic"}}]
# warning: the section Hooks has no Terraform equivalent and isn't converted
# It was: {"Deploy":{"Type":"AWS::CodeDeploy::BlueGreen"}}

# Seeder (Custom::DatabaseSeeder)
# error: a custom resource isn't converted: CloudFormation calls a Lambda function or an SNS topic to create, update and delete it, which Terraform has no equivalent of. Rewrite what the function does as Terraform resources or data sources
# It was:
# {
#   "Type": "Custom::DatabaseSeeder",
#   "Properties": {
#     "ServiceToken": {
#       "Fn::Sub": "arn:aws:lambda:${AWS::Region}:${AWS::AccountId}:function:seeder"
#     },
#     "Rows": 10
#   }
# }

# Child (AWS::CloudFormation::Stack)
# error: a nested stack isn't converted: convert its template on its own and call the result as a module
# It was: {"Type":"AWS::CloudFormation::Stack","Properties":{"TemplateURL":"https://example.com/child.yaml"}}

# Api (AWS::Serverless::Function)
# error: a SAM resource isn't converted: the AWS::Serverless transform expands it into other resources, so convert the processed template (aws cloudformation get-template --template-stage Processed)
# It was: {"Type":"AWS::Serverless::Function","Properties":{"Handler":"index.handler"}}

# Widget (Acme::Widgets::Widget)
# error: the mapper has no Terraform resource type for Acme::Widgets::Widget, so it isn't converted
# It was: {"Type":"Acme::Widgets::Widget","Properties":{"Size":3}}

# CDKMetadata (AWS::CDK::Metadata)
# the CDK's metadata resource, which reports the CDK version to AWS, is left out

# Queue (AWS::SQS::Queue)
# warning: the AWS::CloudFormation::Init metadata isn't converted: cfn-init reads it from the stack on the instance. Move what it configures into the user data or a configuration management tool
# warning: the CreationPolicy attribute has no Terraform equivalent and isn't converted
# It was: {"ResourceSignal":{"Count":1}}
resource "aws_sqs_queue" "queue" {
  # error: Fn::Bogus isn't a function the converter knows; it is null instead
  # It was: {"Fn::Bogus":["a","b"]}
  name = null
  # error: Fn::GetAtt of Seeder.Delay (Custom::DatabaseSeeder), which isn't converted; it is null instead
  delay_seconds = null
  # error: Ref to Missing, which isn't a parameter or a resource of the template; it is null instead
  message_retention_seconds = null
  # error: Fn::FindInMap of "NoSuchMap", which isn't a mapping of the template; it is null instead
  visibility_timeout_seconds = null
  # error: Fn::Transform runs a macro when CloudFormation processes the template, and isn't converted; it is null instead: convert the processed template
  # It was: {"Fn::Transform":{"Name":"AWS::Include","Parameters":{"Location":"s3://bucket/redrive.json"}}}
  redrive_policy = null
}

# ---- outputs.tf ----

output "seeder_result" {
  # error: Fn::GetAtt of Seeder.Result (Custom::DatabaseSeeder), which isn't converted; it is null instead
  value = null
}

output "queue_url" {
  # error: there is no condition NoSuchCondition; it is false instead
  value = false ? aws_sqs_queue.queue.url : null
}

# ---- variables.tf ----

# warning: the template's rules (ProdNeedsLargeInstances) aren't converted: write them as validation blocks on the variables, or as check blocks

variable "environment" {
  type = string
}

variable "instance_type" {
  type = string
}

# ---- diagnostics ----
# error: Transform: the template uses the AWS::Serverless-2016-10-31 transform, which CloudFormation expands into other resources before it deploys them: convert the processed template instead (aws cloudformation get-template --template-stage Processed)
# error: Transform: the template uses the macro AcmeMacro, which CloudFormation runs on the template before it deploys it: convert the processed template instead (aws cloudformation get-template --template-stage Processed)
# error: Resources.Fn::ForEach::Topics: Fn::ForEach isn't converted: it repeats an entry when CloudFormation processes the template. Convert the processed template, or write the entry once with for_each
# warning: Hooks: the section Hooks has no Terraform equivalent and isn't converted
# info: Metadata: the template's Metadata isn't carried over: Terraform has no place for it
# warning: Rules: the template's rules (ProdNeedsLargeInstances) aren't converted: write them as validation blocks on the variables, or as check blocks
# error: Resources.Seeder: a custom resource isn't converted: CloudFormation calls a Lambda function or an SNS topic to create, update and delete it, which Terraform has no equivalent of. Rewrite what the function does as Terraform resources or data sources
# error: Resources.Child: a nested stack isn't converted: convert its template on its own and call the result as a module
# error: Resources.Api: a SAM resource isn't converted: the AWS::Serverless transform expands it into other resources, so convert the processed template (aws cloudformation get-template --template-stage Processed)
# error: Resources.Widget: the mapper has no Terraform resource type for Acme::Widgets::Widget, so it isn't converted
# info: Resources.CDKMetadata: the CDK's metadata resource, which reports the CDK version to AWS, is left out
# warning: Resources.Queue.Metadata: the AWS::CloudFormation::Init metadata isn't converted: cfn-init reads it from the stack on the instance. Move what it configures into the user data or a configuration management tool
# info: Resources.Queue.Metadata: the resource's Metadata isn't carried over: AWS::CloudFormation::Init
# warning: Resources.Queue.CreationPolicy: the CreationPolicy attribute has no Terraform equivalent and isn't converted
# error: Resources.Queue.Properties.QueueName.Fn::Bogus: Fn::Bogus isn't a function the converter knows; it is null instead
# error: Resources.Queue.Properties.DelaySeconds.Fn::GetAtt: Fn::GetAtt of Seeder.Delay (Custom::DatabaseSeeder), which isn't converted; it is null instead
# error: Resources.Queue.Properties.MessageRetentionPeriod.Ref: Ref to Missing, which isn't a parameter or a resource of the template; it is null instead
# error: Resources.Queue.Properties.VisibilityTimeout.Fn::FindInMap: Fn::FindInMap of "NoSuchMap", which isn't a mapping of the template; it is null instead
# error: Resources.Queue.Properties.RedrivePolicy.Fn::Transform: Fn::Transform runs a macro when CloudFormation processes the template, and isn't converted; it is null instead: convert the processed template
# error: Outputs.SeederResult.Value.Fn::GetAtt: Fn::GetAtt of Seeder.Result (Custom::DatabaseSeeder), which isn't converted; it is null instead
# error: Outputs.QueueUrl.Condition: there is no condition NoSuchCondition; it is false instead
