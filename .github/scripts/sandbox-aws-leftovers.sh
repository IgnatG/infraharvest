#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The scheduled clean-up's last word on the AWS sandbox
# (sandbox-leftovers.yml), after sandbox-aws-sweep.sh: finds, without a
# list of kinds kept by hand, what is still the sandbox's, and fails when
# anything is.
#
#   - Every ARN the Resource Groups Tagging API lists with either sandbox
#     tag key, infraharvest-sandbox or infraharvest-sandbox-run, in each
#     region given (the sandbox's two, and us-east-1, where it lists
#     CloudFront and Route 53).
#   - What the Tagging API doesn't list: IAM roles named ih-sbx- that carry
#     either key, and the log groups Lambda names after the CloudFormation
#     estate's ih-sbx- function, which take no tags.
#
# The Tagging API lists deleted resources for a while, so each ARN is
# read again, from its own service or through the Cloud Control API, and
# what is gone doesn't count. Nor do KMS keys already pending deletion, a
# run's usual remains: the sweeps only schedule keys' deletion. It looks
# a few times, a minute apart, before it gives up on what is still going.
#
# With --delete, it first deletes, through the Cloud Control API, every
# tagged resource of a kind sandbox-aws-sweep.sh doesn't delete itself
# and whose CloudFormation type the ARN names beyond doubt; any other is
# reported as it is. It never touches anything without a sandbox tag.
#
# What is left is printed as "<region> <type> <name>", one per line, and
# added to $LEFTOVERS_FILE when it is set: names and IDs only, never an
# account ID, as the logs of a public repository's runs are public.
#
# Usage: [LEFTOVERS_FILE=<file>] sandbox-aws-leftovers.sh [--delete] <region>...
# Signed in as the fixture role. Not -e: one failed call mustn't stop the
# rest.
set -uo pipefail

delete=false
if [ "${1-}" = --delete ]; then
  delete=true
  shift
fi
if [ "$#" -eq 0 ]; then
  echo "usage: $0 [--delete] <region>..." >&2
  exit 2
fi
mapfile -t regions < <(printf '%s\n' "$@" | sed '/^$/d' | sort -u)
keys=(infraharvest-sandbox infraharvest-sandbox-run)
err=$(mktemp)
trap 'rm -f "$err"' EXIT

# tagged prints every ARN in region $1 that carries a sandbox tag key, one
# per line, and fails if the Tagging API does.
tagged() {
  local key list all=""
  for key in "${keys[@]}"; do
    list=$(aws resourcegroupstaggingapi get-resources --region "$1" --tag-filters "Key=$key" \
      --query 'ResourceTagMappingList[].ResourceARN' --output text) || return 1
    all+=$'\n'"$list"
  done
  tr '\t' '\n' <<< "$all" | sed '/^$/d;/^None$/d' | sort -u
}

# cloudcontrol prints the CloudFormation type and the Cloud Control
# identifier of ARN $1, tab-separated, for the kinds whose ARN names both
# beyond doubt, and fails for any other.
cloudcontrol() {
  local service region account resource id kind name cluster nodegroup
  IFS=: read -r _ _ service region account resource <<< "$1"
  id=${resource#*/}
  case "$service:$resource" in
    ec2:vpc/*) printf 'AWS::EC2::VPC\t%s\n' "$id" ;;
    ec2:subnet/*) printf 'AWS::EC2::Subnet\t%s\n' "$id" ;;
    ec2:security-group/*) printf 'AWS::EC2::SecurityGroup\t%s\n' "$id" ;;
    ec2:internet-gateway/*) printf 'AWS::EC2::InternetGateway\t%s\n' "$id" ;;
    ec2:route-table/*) printf 'AWS::EC2::RouteTable\t%s\n' "$id" ;;
    ec2:natgateway/*) printf 'AWS::EC2::NatGateway\t%s\n' "$id" ;;
    ec2:vpc-endpoint/*) printf 'AWS::EC2::VPCEndpoint\t%s\n' "$id" ;;
    ec2:vpc-flow-log/*) printf 'AWS::EC2::FlowLog\t%s\n' "$id" ;;
    ec2:instance/*) printf 'AWS::EC2::Instance\t%s\n' "$id" ;;
    ec2:volume/*) printf 'AWS::EC2::Volume\t%s\n' "$id" ;;
    ec2:network-interface/*) printf 'AWS::EC2::NetworkInterface\t%s\n' "$id" ;;
    ec2:network-acl/*) printf 'AWS::EC2::NetworkAcl\t%s\n' "$id" ;;
    ec2:launch-template/*) printf 'AWS::EC2::LaunchTemplate\t%s\n' "$id" ;;
    logs:log-group:*)
      id=${resource#log-group:}
      printf 'AWS::Logs::LogGroup\t%s\n' "${id%:\*}"
      ;;
    lambda:function:*)
      id=${resource#function:}
      printf 'AWS::Lambda::Function\t%s\n' "${id%%:*}"
      ;;
    sns:*:*) return 1 ;; # a subscription, which goes with its topic
    sns:*) printf 'AWS::SNS::Topic\t%s\n' "$1" ;;
    sqs:*) printf 'AWS::SQS::Queue\thttps://sqs.%s.amazonaws.com/%s/%s\n' "$region" "$account" "$resource" ;;
    s3:*) printf 'AWS::S3::Bucket\t%s\n' "$resource" ;;
    dynamodb:table/*/*) return 1 ;; # a stream or an index of a table
    dynamodb:table/*) printf 'AWS::DynamoDB::Table\t%s\n' "$id" ;;
    kinesis:stream/*) printf 'AWS::Kinesis::Stream\t%s\n' "$id" ;;
    secretsmanager:secret:*) printf 'AWS::SecretsManager::Secret\t%s\n' "$1" ;;
    states:stateMachine:*) printf 'AWS::StepFunctions::StateMachine\t%s\n' "$1" ;;
    events:rule/*/*) return 1 ;; # a rule of a bus other than the default
    events:rule/*) printf 'AWS::Events::Rule\t%s\n' "$1" ;;
    cloudwatch:alarm:*) printf 'AWS::CloudWatch::Alarm\t%s\n' "${resource#alarm:}" ;;
    ecs:cluster/*) printf 'AWS::ECS::Cluster\t%s\n' "$id" ;;
    ecs:task-definition/*) printf 'AWS::ECS::TaskDefinition\t%s\n' "$1" ;;
    elasticloadbalancing:loadbalancer/app/* | elasticloadbalancing:loadbalancer/net/*)
      printf 'AWS::ElasticLoadBalancingV2::LoadBalancer\t%s\n' "$1"
      ;;
    elasticloadbalancing:targetgroup/*) printf 'AWS::ElasticLoadBalancingV2::TargetGroup\t%s\n' "$1" ;;
    elasticloadbalancing:listener/*) printf 'AWS::ElasticLoadBalancingV2::Listener\t%s\n' "$1" ;;
    elasticloadbalancing:listener-rule/*) printf 'AWS::ElasticLoadBalancingV2::ListenerRule\t%s\n' "$1" ;;
    rds:db:*) printf 'AWS::RDS::DBInstance\t%s\n' "${resource#db:}" ;;
    rds:cluster:*) printf 'AWS::RDS::DBCluster\t%s\n' "${resource#cluster:}" ;;
    rds:subgrp:*) printf 'AWS::RDS::DBSubnetGroup\t%s\n' "${resource#subgrp:}" ;;
    rds:pg:*) printf 'AWS::RDS::DBParameterGroup\t%s\n' "${resource#pg:}" ;;
    eks:cluster/*) printf 'AWS::EKS::Cluster\t%s\n' "$id" ;;
    eks:nodegroup/*)
      IFS=/ read -r _ cluster nodegroup _ <<< "$resource"
      printf 'AWS::EKS::Nodegroup\t%s|%s\n' "$cluster" "$nodegroup"
      ;;
    redshift:cluster:*) printf 'AWS::Redshift::Cluster\t%s\n' "${resource#cluster:}" ;;
    redshift:subnetgroup:*) printf 'AWS::Redshift::ClusterSubnetGroup\t%s\n' "${resource#subnetgroup:}" ;;
    elasticache:subnetgroup:*) printf 'AWS::ElastiCache::SubnetGroup\t%s\n' "${resource#subnetgroup:}" ;;
    elasticache:parametergroup:*) printf 'AWS::ElastiCache::ParameterGroup\t%s\n' "${resource#parametergroup:}" ;;
    apigateway:/restapis/*/*) return 1 ;;
    apigateway:/restapis/*) printf 'AWS::ApiGateway::RestApi\t%s\n' "${resource#/restapis/}" ;;
    apigateway:/apis/*/*) return 1 ;;
    apigateway:/apis/*) printf 'AWS::ApiGatewayV2::Api\t%s\n' "${resource#/apis/}" ;;
    route53:hostedzone/*) printf 'AWS::Route53::HostedZone\t%s\n' "$id" ;;
    cloudfront:distribution/*) printf 'AWS::CloudFront::Distribution\t%s\n' "$id" ;;
    wafv2:regional/webacl/* | wafv2:regional/ipset/*)
      IFS=/ read -r _ kind name id <<< "$resource"
      if [ "$kind" = webacl ]; then
        printf 'AWS::WAFv2::WebACL\t%s|%s|REGIONAL\n' "$name" "$id"
      else
        printf 'AWS::WAFv2::IPSet\t%s|%s|REGIONAL\n' "$name" "$id"
      fi
      ;;
    *) return 1 ;;
  esac
}

# swept tells whether sandbox-aws-sweep.sh deletes ARN $1's kind itself,
# in the order its dependencies need: those are never deleted here.
swept() {
  local service resource
  IFS=: read -r _ _ service _ _ resource <<< "$1"
  case "$service:$resource" in
    route53:hostedzone/* | cloudfront:distribution/* | rds:db:* | rds:cluster:* | rds:subgrp:* | rds:pg:* | \
      eks:cluster/* | redshift:cluster:* | redshift:subnetgroup:* | elasticache:replicationgroup:* | \
      elasticache:subnetgroup:* | elasticache:parametergroup:* | elasticloadbalancing:loadbalancer/* | \
      elasticloadbalancing:targetgroup/* | ecs:service/* | ecs:cluster/* | ecs:task-definition/* | \
      wafv2:regional/webacl/* | wafv2:regional/ipset/* | lambda:function:* | apigateway:* | states:stateMachine:* | \
      dynamodb:table/* | kinesis:stream/* | sqs:* | sns:* | s3:* | secretsmanager:secret:* | logs:log-group:* | \
      cloudwatch:alarm:* | events:rule/* | ec2:vpc-flow-log/* | ssm:parameter/ih-sbx-* | cloudformation:stack/* | \
      kms:key/* | ec2:instance/* | ec2:natgateway/* | ec2:elastic-ip/* | ec2:vpc-endpoint/* | \
      ec2:internet-gateway/* | ec2:route-table/* | ec2:security-group/* | ec2:subnet/* | ec2:vpc/*)
      return 0
      ;;
  esac
  return 1
}

# missing tells whether the read that last wrote to $err said the
# resource doesn't exist; any other error leaves it counted.
missing() {
  grep -qiE 'not ?found|does not exist|NoSuch|InvalidParameterValue.*not exist' "$err"
}

# gone tells whether ARN $1, in region $2, is deleted, being deleted, or a
# KMS key already scheduled for deletion: what doesn't count.
gone() {
  local arn=$1 region=$2 service resource state type id
  IFS=: read -r _ _ service _ _ resource <<< "$arn"
  case "$service:$resource" in
    kms:key/*)
      state=$(aws kms describe-key --region "$region" --key-id "$arn" \
        --query 'KeyMetadata.[KeyManager,KeyState]' --output text 2> "$err") || { missing; return; }
      case "$state" in
        CUSTOMER*Enabled | CUSTOMER*Disabled | CUSTOMER*PendingImport | CUSTOMER*Creating) return 1 ;;
      esac
      return 0
      ;;
    ec2:instance/*)
      state=$(aws ec2 describe-instances --region "$region" --instance-ids "${resource#instance/}" \
        --query 'Reservations[].Instances[].State.Name' --output text 2> "$err") || { missing; return; }
      [ "$state" = terminated ] || [ "$state" = shutting-down ] || [ -z "$state" ]
      return
      ;;
    ec2:natgateway/*)
      state=$(aws ec2 describe-nat-gateways --region "$region" --nat-gateway-ids "${resource#natgateway/}" \
        --query 'NatGateways[].State' --output text 2> "$err") || { missing; return; }
      [ "$state" = deleted ] || [ "$state" = deleting ] || [ "$state" = failed ] || [ -z "$state" ]
      return
      ;;
    ec2:vpc-endpoint/*)
      state=$(aws ec2 describe-vpc-endpoints --region "$region" --vpc-endpoint-ids "${resource#vpc-endpoint/}" \
        --query 'VpcEndpoints[].State' --output text 2> "$err") || { missing; return; }
      [ "$state" = deleted ] || [ "$state" = deleting ] || [ -z "$state" ]
      return
      ;;
    ec2:security-group-rule/*)
      aws ec2 describe-security-group-rules --region "$region" \
        --security-group-rule-ids "${resource#security-group-rule/}" > /dev/null 2> "$err" && return 1
      missing
      return
      ;;
    ec2:elastic-ip/*)
      aws ec2 describe-addresses --region "$region" --allocation-ids "${resource#elastic-ip/}" > /dev/null 2> "$err" && return 1
      missing
      return
      ;;
    cloudformation:stack/*)
      state=$(aws cloudformation describe-stacks --region "$region" --stack-name "$arn" \
        --query 'Stacks[].StackStatus' --output text 2> "$err") || { missing; return; }
      [ "$state" = DELETE_COMPLETE ] || [ "$state" = DELETE_IN_PROGRESS ]
      return
      ;;
    ecs:service/*)
      state=$(aws ecs describe-services --region "$region" --cluster "$(cut -d/ -f2 <<< "$resource")" \
        --services "$arn" --query 'services[].status' --output text 2> "$err") || { missing; return; }
      [ "$state" != ACTIVE ]
      return
      ;;
    elasticache:replicationgroup:*)
      state=$(aws elasticache describe-replication-groups --region "$region" \
        --replication-group-id "${resource#replicationgroup:}" \
        --query 'ReplicationGroups[].Status' --output text 2> "$err") || { missing; return; }
      [ "$state" = deleting ]
      return
      ;;
    ssm:parameter/*)
      aws ssm get-parameter --region "$region" --name "/${resource#parameter/}" > /dev/null 2> "$err" && return 1
      aws ssm get-parameter --region "$region" --name "${resource#parameter/}" > /dev/null 2> "$err" && return 1
      missing
      return
      ;;
    apigateway:/restapis/*/stages/*)
      IFS=/ read -r _ _ id _ state <<< "$resource"
      aws apigateway get-stage --region "$region" --rest-api-id "$id" --stage-name "$state" > /dev/null 2> "$err" && return 1
      missing
      return
      ;;
    apigateway:/apis/*/stages/*)
      IFS=/ read -r _ _ id _ state <<< "$resource"
      aws apigatewayv2 get-stage --region "$region" --api-id "$id" --stage-name "$state" > /dev/null 2> "$err" && return 1
      missing
      return
      ;;
  esac
  if IFS=$'\t' read -r type id < <(cloudcontrol "$arn"); then
    aws cloudcontrol get-resource --region "$region" --type-name "$type" --identifier "$id" > /dev/null 2> "$err" && return 1
    missing
    return
  fi
  return 1
}

# describe prints ARN $1 as "<type> <name>": its CloudFormation type, or
# its service and kind, and its name or ID, without the account.
describe() {
  local service resource type id
  IFS=: read -r _ _ service _ _ resource <<< "$1"
  if IFS=$'\t' read -r type id < <(cloudcontrol "$1"); then
    printf '%s %s\n' "$type" "${resource#*[/:]}"
  else
    printf '%s:%s %s\n' "$service" "${resource%%[/:]*}" "${resource#*[/:]}"
  fi
}

# Deletes, through Cloud Control, what the sweep doesn't, and waits for
# each delete to finish.
if [ "$delete" = true ]; then
  for region in "${regions[@]}"; do
    if ! arns=$(tagged "$region"); then
      echo "::error::The Tagging API failed in $region"
      continue
    fi
    tokens=()
    while IFS= read -r arn; do
      if [ -z "$arn" ] || swept "$arn" || gone "$arn" "$region"; then
        continue
      fi
      if IFS=$'\t' read -r type id < <(cloudcontrol "$arn"); then
        echo "deleting $region $(describe "$arn") through Cloud Control"
        token=$(aws cloudcontrol delete-resource --region "$region" --type-name "$type" --identifier "$id" \
          --query 'ProgressEvent.RequestToken' --output text) && tokens+=("$token")
      else
        echo "no known way to delete $region $(describe "$arn"): it is reported"
      fi
    done <<< "$arns"
    for token in "${tokens[@]}"; do
      aws cloudcontrol wait resource-request-success --region "$region" --request-token "$token" ||
        echo "failed: Cloud Control request $token in $region"
    done
  done
fi

# What is left, looked for a few times, a minute apart, while deletes
# finish and the Tagging API catches up.
left=()
for round in 1 2 3 4 5; do
  if [ "$round" -gt 1 ]; then
    echo "${#left[@]} left; looking again in a minute"
    sleep 60
  fi
  left=()
  failed=false
  for region in "${regions[@]}"; do
    if ! arns=$(tagged "$region"); then
      echo "::error::The Tagging API failed in $region"
      failed=true
      continue
    fi
    while IFS= read -r arn; do
      if [ -n "$arn" ] && ! gone "$arn" "$region"; then
        left+=("$region $(describe "$arn")")
      fi
    done <<< "$arns"
    groups=$(aws logs describe-log-groups --region "$region" --log-group-name-prefix /aws/lambda/ih-sbx- \
      --query "logGroups[?ends_with(logGroupName, '-cfn-seed')].logGroupName" --output text) || failed=true
    for group in $(tr '\t' '\n' <<< "$groups" | sed '/^$/d;/^None$/d'); do
      left+=("$region AWS::Logs::LogGroup $group")
    done
  done
  # IAM is global, and the Tagging API doesn't list roles.
  roles=$(aws iam list-roles --query "Roles[?starts_with(RoleName, 'ih-sbx-')].RoleName" --output text) || failed=true
  for role in $(tr '\t' '\n' <<< "$roles" | sed '/^$/d;/^None$/d'); do
    tags=$(aws iam list-role-tags --role-name "$role" \
      --query "Tags[?Key=='infraharvest-sandbox' || Key=='infraharvest-sandbox-run'].Key" --output text) || failed=true
    if [ -n "$tags" ] && [ "$tags" != None ]; then
      left+=("global AWS::IAM::Role $role")
    fi
  done
  if [ "${#left[@]}" -eq 0 ] && [ "$failed" = false ]; then
    break
  fi
done

if [ -n "${LEFTOVERS_FILE-}" ]; then
  printf '%s\n' "${left[@]}" | sed '/^$/d' >> "$LEFTOVERS_FILE"
fi
if [ "$failed" = true ]; then
  echo "::error::A listing failed: what is left may be more than this says"
fi
if [ "${#left[@]}" -gt 0 ]; then
  echo "::error::Left in AWS after the clean-up:"
  printf '%s\n' "${left[@]}"
  exit 1
fi
if [ "$failed" = true ]; then
  exit 1
fi
echo "Nothing left in AWS."
