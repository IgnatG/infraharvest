#!/usr/bin/env bash
# Copyright 2026 Gabriel Ignat
# SPDX-License-Identifier: AGPL-3.0-only

# The sandbox-aws workflow's last resort, after the estate's destroy, and
# what its clean-up job runs when the round trip was cancelled, timed out
# or failed: finds what is still tagged infraharvest-sandbox-run=$RUN_ID
# and deletes it, in the order its dependencies allow, without final
# snapshots:
#
#   1. Route 53 hosted zones, with their records, and CloudFront
#      distributions, which are disabled now and deleted last, once
#      CloudFront has deployed them disabled. Both are global, and the
#      Tagging API lists them in us-east-1.
#   2. In each region, what runs or holds the network: EC2 instances, NAT
#      gateways, load balancers, ECS services, RDS instances and clusters,
#      Redshift clusters, ElastiCache replication groups and EKS clusters.
#   3. What uses nothing else of the estate: Lambda functions, REST and
#      HTTP APIs, Step Functions state machines, DynamoDB tables, with
#      their replicas in other regions, Kinesis streams, SQS queues, SNS
#      topics, S3 buckets, Secrets Manager secrets, CloudWatch log groups
#      and alarms, EventBridge rules, SSM parameters named /ih-sbx-...,
#      which would fail the next run's create, ECS task definitions,
#      CloudFormation stacks, and KMS keys, which are scheduled for
#      deletion after the shortest wait, 7 days, with their aliases.
#   4. What the first waited for: ECS clusters, load balancers' target
#      groups and WAF web ACLs and IP sets, Elastic IPs, and the RDS,
#      ElastiCache and Redshift subnet and parameter groups.
#   5. The network, a few rounds over, as ENIs that load balancers and
#      NAT gateways leave go a while after them: VPC endpoints, internet
#      gateways, route tables, security groups, subnets and VPCs.
#   6. The CloudFront distributions disabled in 1, and IAM roles named
#      ih-sbx-* that carry a run tag, with their instance profiles; IAM is
#      global and the Tagging API doesn't list roles.
#
# It sweeps what earlier runs left with their own run tag the same way:
# runs never overlap (the workflow's concurrency group), so another run's
# tag only remains on what that run failed to delete, as when it was
# cancelled during its destroy. It touches nothing without the tag but the
# log groups Lambda names after the CloudFormation estate's ih-sbx-
# function, and the aliases and instance profiles named ih-sbx- of the
# keys and roles it deletes. What carries no tags of its own (route table
# associations, security group rules, the WAF association, listeners,
# Route 53 records, API stages, metric filters) goes with what holds it.
# Deletes take a while, and the Tagging API lists some deleted resources
# for a while too (KMS keys until they are gone), so what it warns about
# at the end may already be going. It prints names and ARNs only.
#
# Usage: RUN_ID=<run id> sandbox-aws-sweep.sh <region>...
# Not -e: one failed call mustn't stop the sweep of the rest.
set -uo pipefail

key=infraharvest-sandbox-run
# An empty value would match every value of the key.
own=${RUN_ID:?RUN_ID names the run whose resources to sweep}
if [ "$#" -eq 0 ]; then
  echo "usage: RUN_ID=<run id> $0 <region>..." >&2
  exit 2
fi

# runs prints the runs whose tag is still on something in region $1, one
# per line.
runs() {
  aws resourcegroupstaggingapi get-resources --region "$1" --tag-filters "Key=$key" --output json |
    jq -r --arg key "$key" '.ResourceTagMappingList[].Tags[] | select(.Key == $key and .Value != "") | .Value'
}

# tagged prints the ARNs in region $1 tagged with run $2, one per line.
tagged() {
  aws resourcegroupstaggingapi get-resources --region "$1" \
    --tag-filters "Key=$key,Values=$2" \
    --query 'ResourceTagMappingList[].ResourceARN' --output text | tr '\t' '\n' | sed '/^$/d'
}

# try runs a delete, and says so rather than stopping when it fails, as it
# does on a resource already being deleted.
try() {
  if ! "$@" > /dev/null; then
    echo "failed: $*"
  fi
}

# again runs a delete up to 30 times, 10 seconds apart, for one that waits
# on another to finish going, and says so if it never succeeds.
again() {
  for _ in $(seq 30); do
    if "$@" > /dev/null 2>&1; then
      return 0
    fi
    sleep 10
  done
  echo "failed: $*"
}

# words prints the words of $1, one per line: the AWS CLI's text output
# separates them with tabs.
words() {
  tr '\t' '\n' <<< "$1" | sed '/^$/d'
}

# ec2_ids prints the IDs of EC2 resources of the run being swept in
# region $1: $2 is the describe command, $3 its query, and the rest more
# filters.
ec2_ids() {
  local region=$1 command=$2 query=$3
  shift 3
  words "$(aws ec2 "$command" --region "$region" --filters "Name=tag:$key,Values=$run" "$@" --query "$query" --output text)"
}

# empty_bucket deletes every object version and delete marker in bucket $1,
# in region $2, a thousand at a time.
empty_bucket() {
  local batch
  for _ in $(seq 1000); do
    batch=$(aws s3api list-object-versions --region "$2" --bucket "$1" --max-items 1000 \
      --query '{Objects: [Versions[], DeleteMarkers[]][].{Key: Key, VersionId: VersionId}}' \
      --output json) || return 1
    if [ "$(jq '.Objects | length' <<< "$batch")" -eq 0 ]; then
      return 0
    fi
    aws s3api delete-objects --region "$2" --bucket "$1" --delete "$batch" > /dev/null || return 1
  done
}

# delete_zone deletes the records of hosted zone $1 but its own SOA and NS,
# which go with it, and then the zone.
delete_zone() {
  local zone=$1 apex changes
  apex=$(aws route53 get-hosted-zone --id "$zone" --query HostedZone.Name --output text) || return 1
  changes=$(aws route53 list-resource-record-sets --hosted-zone-id "$zone" --output json |
    jq -c --arg apex "$apex" '[.ResourceRecordSets[]
      | select((.Type == "SOA" or .Type == "NS") and .Name == $apex | not)
      | {Action: "DELETE", ResourceRecordSet: .}]')
  if [ -n "$changes" ] && [ "$changes" != "[]" ]; then
    try aws route53 change-resource-record-sets --hosted-zone-id "$zone" --change-batch "{\"Changes\": $changes}"
  fi
  try aws route53 delete-hosted-zone --id "$zone"
}

# disable_distribution disables CloudFront distribution $1, which can be
# deleted only once it is deployed disabled.
disable_distribution() {
  local config etag
  config=$(aws cloudfront get-distribution-config --id "$1" --output json) || return 1
  if [ "$(jq '.DistributionConfig.Enabled' <<< "$config")" = true ]; then
    etag=$(jq -r .ETag <<< "$config")
    try aws cloudfront update-distribution --id "$1" --if-match "$etag" \
      --distribution-config "$(jq -c '.DistributionConfig | .Enabled = false' <<< "$config")"
  fi
}

# delete_distribution waits for CloudFront distribution $1 to be deployed
# disabled, and deletes it.
delete_distribution() {
  local etag
  try aws cloudfront wait distribution-deployed --id "$1"
  etag=$(aws cloudfront get-distribution --id "$1" --query ETag --output text) || return 1
  try aws cloudfront delete-distribution --id "$1" --if-match "$etag"
}

# delete_table deletes DynamoDB table $1 in region $2. A replica of a
# global table is deleted as any table is, but the table its replicas were
# added from can't be deleted while they are there, for a day after: so
# when the delete fails, the table's replicas in other regions are removed,
# one at a time as UpdateTable allows, and it is deleted once they are
# gone, up to ten minutes for each.
delete_table() {
  local table=$1 region=$2 replicas replica
  if aws dynamodb delete-table --region "$region" --table-name "$table" > /dev/null 2>&1; then
    return 0
  fi
  replicas=$(aws dynamodb describe-table --region "$region" --table-name "$table" \
    --query "Table.Replicas[?RegionName != '$region'].RegionName" --output text) || { echo "failed: deleting DynamoDB table $table in $region"; return 1; }
  for replica in $(words "$replicas"); do
    echo "removing its replica in $replica"
    again aws dynamodb update-table --region "$region" --table-name "$table" \
      --replica-updates "[{\"Delete\": {\"RegionName\": \"$replica\"}}]"
    for _ in $(seq 60); do
      if [ -z "$(aws dynamodb describe-table --region "$region" --table-name "$table" \
        --query "Table.Replicas[?RegionName == '$replica'].RegionName" --output text)" ]; then
        break
      fi
      sleep 10
    done
  done
  again aws dynamodb delete-table --region "$region" --table-name "$table"
}

# Each region once.
mapfile -t regions < <(printf '%s\n' "$@" | sed '/^$/d' | sort -u)

# 1. Global: Route 53 and CloudFront, which the Tagging API lists in
# us-east-1.
distributions=()
mapfile -t global_runs < <({ echo "$own"; runs us-east-1; } | sed '/^$/d' | sort -u)
for run in "${global_runs[@]}"; do
  while read -r arn <&3; do
    IFS=: read -r _ _ service _ _ resource <<< "$arn"
    case "$service:$resource" in
      route53:hostedzone/*)
        echo "deleting Route 53 hosted zone ${resource#hostedzone/}"
        delete_zone "${resource#hostedzone/}"
        ;;
      cloudfront:distribution/*)
        echo "disabling CloudFront distribution ${resource#distribution/}"
        disable_distribution "${resource#distribution/}"
        distributions+=("${resource#distribution/}")
        ;;
    esac
  done 3< <(tagged us-east-1 "$run")
done

for region in "${regions[@]}"; do
  mapfile -t swept < <({ echo "$own"; runs "$region"; } | sed '/^$/d' | sort -u)
  for run in "${swept[@]}"; do
    if [ "$run" = "$own" ]; then
      echo "== $region"
    else
      echo "::warning::$key=$run, an earlier run, still tags resources in $region, or the Tagging API still lists them: sweeping them"
    fi

    # 2. What runs or holds the network. EC2 from its own API, by state:
    # the Tagging API lists terminated instances and deleted NAT gateways
    # for a while.
    mapfile -t instances < <(ec2_ids "$region" describe-instances 'Reservations[].Instances[].InstanceId' \
      Name=instance-state-name,Values=pending,running,stopping,stopped)
    if [ "${#instances[@]}" -gt 0 ]; then
      echo "terminating instances: ${instances[*]}"
      try aws ec2 terminate-instances --region "$region" --instance-ids "${instances[@]}"
    fi
    mapfile -t nats < <(words "$(aws ec2 describe-nat-gateways --region "$region" \
      --filter "Name=tag:$key,Values=$run" Name=state,Values=pending,available \
      --query 'NatGateways[].NatGatewayId' --output text)")
    for nat in "${nats[@]}"; do
      echo "deleting NAT gateway $nat"
      try aws ec2 delete-nat-gateway --region "$region" --nat-gateway-id "$nat"
    done

    rds_instances=()
    rds_clusters=()
    rds_groups=()
    rds_parameters=()
    redshift_clusters=()
    redshift_groups=()
    cache_groups=()
    cache_subnets=()
    cache_parameters=()
    eks_clusters=()
    ecs_clusters=()
    target_groups=()
    web_acls=()
    ip_sets=()
    mapfile -t arns < <(tagged "$region" "$run")
    for arn in "${arns[@]}"; do
      # arn:partition:service:region:account:resource, where the resource
      # may hold more colons.
      IFS=: read -r _ _ service _ _ resource <<< "$arn"
      case "$service:$resource" in
        rds:db:*) rds_instances+=("${resource#db:}") ;;
        rds:cluster:*) rds_clusters+=("${resource#cluster:}") ;;
        rds:subgrp:*) rds_groups+=("${resource#subgrp:}") ;;
        rds:pg:*) rds_parameters+=("${resource#pg:}") ;;
        eks:cluster/*) eks_clusters+=("${resource#cluster/}") ;;
        redshift:cluster:*)
          echo "deleting Redshift cluster ${resource#cluster:}"
          try aws redshift delete-cluster --region "$region" --cluster-identifier "${resource#cluster:}" \
            --skip-final-cluster-snapshot
          redshift_clusters+=("${resource#cluster:}")
          ;;
        redshift:subnetgroup:*) redshift_groups+=("${resource#subnetgroup:}") ;;
        elasticache:replicationgroup:*)
          echo "deleting ElastiCache replication group ${resource#replicationgroup:}"
          try aws elasticache delete-replication-group --region "$region" \
            --replication-group-id "${resource#replicationgroup:}"
          cache_groups+=("${resource#replicationgroup:}")
          ;;
        elasticache:subnetgroup:*) cache_subnets+=("${resource#subnetgroup:}") ;;
        elasticache:parametergroup:*) cache_parameters+=("${resource#parametergroup:}") ;;
        elasticloadbalancing:loadbalancer/app/* | elasticloadbalancing:loadbalancer/net/* | elasticloadbalancing:loadbalancer/gwy/*)
          echo "deleting load balancer $arn"
          try aws elbv2 delete-load-balancer --region "$region" --load-balancer-arn "$arn"
          ;;
        elasticloadbalancing:loadbalancer/*)
          echo "deleting classic load balancer ${resource#loadbalancer/}"
          try aws elb delete-load-balancer --region "$region" --load-balancer-name "${resource#loadbalancer/}"
          ;;
        elasticloadbalancing:targetgroup/*) target_groups+=("$arn") ;;
        ecs:service/*)
          IFS=/ read -r _ cluster name <<< "$resource"
          echo "deleting ECS service $cluster/$name"
          try aws ecs delete-service --region "$region" --cluster "$cluster" --service "$name" --force
          ;;
        ecs:cluster/*) ecs_clusters+=("${resource#cluster/}") ;;
        wafv2:regional/webacl/*) web_acls+=("$arn") ;;
        wafv2:regional/ipset/*) ip_sets+=("$arn") ;;
      esac
    done

    # 3. What uses nothing else of the estate.
    for arn in "${arns[@]}"; do
      IFS=: read -r _ _ service _ _ resource <<< "$arn"
      case "$service:$resource" in
        lambda:function:*)
          name=${resource#function:}
          echo "deleting Lambda function ${name%%:*}"
          try aws lambda delete-function --region "$region" --function-name "${name%%:*}"
          ;;
        # A stage is listed apart from its API, and goes with it.
        apigateway:/restapis/*/* | apigateway:/apis/*/*) ;;
        apigateway:/restapis/*)
          echo "deleting REST API ${resource#/restapis/}"
          try aws apigateway delete-rest-api --region "$region" --rest-api-id "${resource#/restapis/}"
          ;;
        apigateway:/apis/*)
          echo "deleting HTTP API ${resource#/apis/}"
          try aws apigatewayv2 delete-api --region "$region" --api-id "${resource#/apis/}"
          ;;
        states:stateMachine:*)
          echo "deleting Step Functions state machine ${resource#stateMachine:}"
          try aws stepfunctions delete-state-machine --region "$region" --state-machine-arn "$arn"
          ;;
        dynamodb:table/*)
          echo "deleting DynamoDB table ${resource#table/}"
          delete_table "${resource#table/}" "$region"
          ;;
        kinesis:stream/*)
          echo "deleting Kinesis stream ${resource#stream/}"
          try aws kinesis delete-stream --region "$region" --stream-name "${resource#stream/}" --enforce-consumer-deletion
          ;;
        sqs:*)
          echo "deleting SQS queue $resource"
          url=$(aws sqs get-queue-url --region "$region" --queue-name "$resource" --query QueueUrl --output text) &&
            try aws sqs delete-queue --region "$region" --queue-url "$url"
          ;;
        sns:*)
          echo "deleting SNS topic $resource"
          try aws sns delete-topic --region "$region" --topic-arn "$arn"
          ;;
        s3:*)
          echo "emptying and deleting S3 bucket $resource"
          if empty_bucket "$resource" "$region"; then
            try aws s3api delete-bucket --region "$region" --bucket "$resource"
          else
            echo "failed to empty $resource"
          fi
          ;;
        secretsmanager:secret:*)
          echo "deleting secret ${resource#secret:}"
          try aws secretsmanager delete-secret --region "$region" --secret-id "$arn" --force-delete-without-recovery
          ;;
        logs:log-group:*)
          name=${resource#log-group:}
          name=${name%:\*}
          echo "deleting log group $name"
          try aws logs delete-log-group --region "$region" --log-group-name "$name"
          ;;
        cloudwatch:alarm:*)
          echo "deleting alarm ${resource#alarm:}"
          try aws cloudwatch delete-alarms --region "$region" --alarm-names "${resource#alarm:}"
          ;;
        # Rules on the default bus, which the estates use.
        events:rule/*)
          name=${resource#rule/}
          echo "deleting EventBridge rule $name"
          mapfile -t targets < <(words "$(aws events list-targets-by-rule --region "$region" --rule "$name" \
            --query 'Targets[].Id' --output text)")
          if [ "${#targets[@]}" -gt 0 ]; then
            try aws events remove-targets --region "$region" --rule "$name" --ids "${targets[@]}"
          fi
          try aws events delete-rule --region "$region" --name "$name"
          ;;
        # The estates name their parameters /ih-sbx-..., which the ARN
        # writes without the leading slash.
        ssm:parameter/ih-sbx-*)
          echo "deleting SSM parameter /${resource#parameter/}"
          try aws ssm delete-parameter --region "$region" --name "/${resource#parameter/}"
          ;;
        ecs:task-definition/*)
          status=$(aws ecs describe-task-definition --region "$region" --task-definition "$arn" \
            --query 'taskDefinition.status' --output text 2> /dev/null)
          echo "deleting ECS task definition ${resource#task-definition/} ($status)"
          if [ "$status" = ACTIVE ]; then
            try aws ecs deregister-task-definition --region "$region" --task-definition "$arn"
          fi
          if [ "$status" = ACTIVE ] || [ "$status" = INACTIVE ]; then
            try aws ecs delete-task-definitions --region "$region" --task-definitions "$arn"
          fi
          ;;
        cloudformation:stack/*)
          IFS=/ read -r _ name _ <<< "$resource"
          echo "deleting CloudFormation stack $name"
          try aws cloudformation delete-stack --region "$region" --stack-name "$arn"
          ;;
        # Only a customer managed key not yet scheduled for deletion, with
        # its ih-sbx- aliases, which take no tags.
        kms:key/*)
          state=$(aws kms describe-key --region "$region" --key-id "$arn" \
            --query 'KeyMetadata.[KeyManager,KeyState]' --output text 2> /dev/null)
          case "$state" in
            CUSTOMER*Enabled | CUSTOMER*Disabled)
              echo "scheduling the deletion of KMS key ${resource#key/}"
              mapfile -t aliases < <(words "$(aws kms list-aliases --region "$region" --key-id "$arn" \
                --query "Aliases[?starts_with(AliasName, 'alias/ih-sbx-')].AliasName" --output text)")
              for alias in "${aliases[@]}"; do
                try aws kms delete-alias --region "$region" --alias-name "$alias"
              done
              try aws kms schedule-key-deletion --region "$region" --key-id "$arn" --pending-window-in-days 7
              ;;
          esac
          ;;
      esac
    done

    # 4. What waited on the above.
    for db in "${rds_instances[@]}"; do
      echo "deleting RDS instance $db"
      try aws rds delete-db-instance --region "$region" --db-instance-identifier "$db" \
        --skip-final-snapshot --delete-automated-backups
    done
    # A cluster, a subnet group or a parameter group goes once its
    # instances have.
    if [ "$((${#rds_clusters[@]} + ${#rds_groups[@]} + ${#rds_parameters[@]}))" -gt 0 ]; then
      for db in "${rds_instances[@]}"; do
        try aws rds wait db-instance-deleted --region "$region" --db-instance-identifier "$db"
      done
    fi
    for cluster in "${rds_clusters[@]}"; do
      echo "deleting RDS cluster $cluster"
      try aws rds delete-db-cluster --region "$region" --db-cluster-identifier "$cluster" --skip-final-snapshot
    done
    for group in "${rds_groups[@]}"; do
      echo "deleting RDS subnet group $group"
      again aws rds delete-db-subnet-group --region "$region" --db-subnet-group-name "$group"
    done
    for group in "${rds_parameters[@]}"; do
      echo "deleting RDS parameter group $group"
      again aws rds delete-db-parameter-group --region "$region" --db-parameter-group-name "$group"
    done
    # An EKS cluster goes once its node groups have, whatever their tags.
    for cluster in "${eks_clusters[@]}"; do
      mapfile -t nodegroups < <(words "$(aws eks list-nodegroups --region "$region" --cluster-name "$cluster" \
        --query 'nodegroups[]' --output text)")
      for nodegroup in "${nodegroups[@]}"; do
        echo "deleting EKS node group $cluster/$nodegroup"
        try aws eks delete-nodegroup --region "$region" --cluster-name "$cluster" --nodegroup-name "$nodegroup"
      done
      for nodegroup in "${nodegroups[@]}"; do
        try aws eks wait nodegroup-deleted --region "$region" --cluster-name "$cluster" --nodegroup-name "$nodegroup"
      done
      echo "deleting EKS cluster $cluster"
      try aws eks delete-cluster --region "$region" --name "$cluster"
    done
    # ElastiCache's and Redshift's groups go once their clusters have.
    if [ "$((${#cache_subnets[@]} + ${#cache_parameters[@]}))" -gt 0 ]; then
      for group in "${cache_groups[@]}"; do
        try aws elasticache wait replication-group-deleted --region "$region" --replication-group-id "$group"
      done
    fi
    for group in "${cache_subnets[@]}"; do
      echo "deleting ElastiCache subnet group $group"
      again aws elasticache delete-cache-subnet-group --region "$region" --cache-subnet-group-name "$group"
    done
    for group in "${cache_parameters[@]}"; do
      echo "deleting ElastiCache parameter group $group"
      again aws elasticache delete-cache-parameter-group --region "$region" --cache-parameter-group-name "$group"
    done
    if [ "${#redshift_groups[@]}" -gt 0 ]; then
      for cluster in "${redshift_clusters[@]}"; do
        try aws redshift wait cluster-deleted --region "$region" --cluster-identifier "$cluster"
      done
    fi
    for group in "${redshift_groups[@]}"; do
      echo "deleting Redshift subnet group $group"
      again aws redshift delete-cluster-subnet-group --region "$region" --cluster-subnet-group-name "$group"
    done
    # An ECS cluster goes once its services have drained.
    for cluster in "${ecs_clusters[@]}"; do
      echo "deleting ECS cluster $cluster"
      again aws ecs delete-cluster --region "$region" --cluster "$cluster"
    done
    # A target group goes once its load balancer's listeners have.
    for group in "${target_groups[@]}"; do
      echo "deleting target group $group"
      again aws elbv2 delete-target-group --region "$region" --target-group-arn "$group"
    done
    # A web ACL goes once nothing uses it, and an IP set once no web ACL
    # does; both are deleted with the lock token of their last read.
    for acl in "${web_acls[@]}"; do
      IFS=/ read -r _ name id <<< "${acl#*:regional/}"
      echo "deleting WAF web ACL $name"
      mapfile -t associated < <(words "$(aws wafv2 list-resources-for-web-acl --region "$region" --web-acl-arn "$acl" \
        --resource-type APPLICATION_LOAD_BALANCER --query 'ResourceArns[]' --output text)")
      for resource in "${associated[@]}"; do
        try aws wafv2 disassociate-web-acl --region "$region" --resource-arn "$resource"
      done
      token=$(aws wafv2 get-web-acl --region "$region" --scope REGIONAL --name "$name" --id "$id" \
        --query LockToken --output text) &&
        again aws wafv2 delete-web-acl --region "$region" --scope REGIONAL --name "$name" --id "$id" --lock-token "$token"
    done
    for set in "${ip_sets[@]}"; do
      IFS=/ read -r _ name id <<< "${set#*:regional/}"
      echo "deleting WAF IP set $name"
      token=$(aws wafv2 get-ip-set --region "$region" --scope REGIONAL --name "$name" --id "$id" \
        --query LockToken --output text) &&
        again aws wafv2 delete-ip-set --region "$region" --scope REGIONAL --name "$name" --id "$id" --lock-token "$token"
    done
    # An Elastic IP is released once its NAT gateway is gone, which may
    # take a little longer to let go of its address.
    if [ "${#nats[@]}" -gt 0 ]; then
      for _ in $(seq 60); do
        pending=$(aws ec2 describe-nat-gateways --region "$region" --nat-gateway-ids "${nats[@]}" \
          --query "NatGateways[?State!='deleted'].NatGatewayId" --output text)
        [ -z "$pending" ] && break
        sleep 10
      done
    fi
    mapfile -t eips < <(ec2_ids "$region" describe-addresses 'Addresses[].AllocationId')
    for eip in "${eips[@]}"; do
      echo "releasing Elastic IP $eip"
      again aws ec2 release-address --region "$region" --allocation-id "$eip"
    done

    # The log group Lambda makes for the CloudFormation estate's custom
    # resource handler has no tags: it is told by its ih-sbx- name alone.
    mapfile -t groups < <(words "$(aws logs describe-log-groups --region "$region" --log-group-name-prefix /aws/lambda/ih-sbx- \
      --query "logGroups[?ends_with(logGroupName, '-cfn-seed')].logGroupName" --output text)")
    for group in "${groups[@]}"; do
      echo "deleting log group $group"
      try aws logs delete-log-group --region "$region" --log-group-name "$group"
    done

    # 5. The network, once the instances have gone, a few rounds over:
    # what load balancers, NAT gateways and functions leave in a subnet
    # goes a few minutes after them.
    if [ "${#instances[@]}" -gt 0 ]; then
      try aws ec2 wait instance-terminated --region "$region" --instance-ids "${instances[@]}"
    fi
    for round in $(seq 20); do
      mapfile -t vpcs < <(ec2_ids "$region" describe-vpcs 'Vpcs[].VpcId')
      if [ "${#vpcs[@]}" -eq 0 ]; then
        break
      fi
      if [ "$round" -gt 1 ]; then
        sleep 30
      fi
      echo "deleting the network of VPCs ${vpcs[*]}, round $round"
      mapfile -t endpoints < <(ec2_ids "$region" describe-vpc-endpoints 'VpcEndpoints[].VpcEndpointId' \
        Name=vpc-endpoint-state,Values=pendingAcceptance,pending,available,rejected,failed)
      if [ "${#endpoints[@]}" -gt 0 ]; then
        try aws ec2 delete-vpc-endpoints --region "$region" --vpc-endpoint-ids "${endpoints[@]}"
      fi
      while read -r gateway attached; do
        if [ -n "$attached" ] && [ "$attached" != None ]; then
          aws ec2 detach-internet-gateway --region "$region" --internet-gateway-id "$gateway" --vpc-id "$attached" > /dev/null 2>&1
        fi
        aws ec2 delete-internet-gateway --region "$region" --internet-gateway-id "$gateway" > /dev/null 2>&1
      done < <(ec2_ids "$region" describe-internet-gateways 'InternetGateways[].[InternetGatewayId, Attachments[0].VpcId]' |
        paste - - 2> /dev/null)
      # The main route table goes with its VPC.
      while read -r table; do
        while read -r association; do
          aws ec2 disassociate-route-table --region "$region" --association-id "$association" > /dev/null 2>&1
        done < <(words "$(aws ec2 describe-route-tables --region "$region" --route-table-ids "$table" \
          --query 'RouteTables[].Associations[?!Main].RouteTableAssociationId' --output text)")
        aws ec2 delete-route-table --region "$region" --route-table-id "$table" > /dev/null 2>&1
      done < <(ec2_ids "$region" describe-route-tables 'RouteTables[?!(Associations[?Main])].RouteTableId')
      while read -r group; do
        aws ec2 delete-security-group --region "$region" --group-id "$group" > /dev/null 2>&1
      done < <(ec2_ids "$region" describe-security-groups "SecurityGroups[?GroupName!='default'].GroupId")
      while read -r subnet; do
        aws ec2 delete-subnet --region "$region" --subnet-id "$subnet" > /dev/null 2>&1
      done < <(ec2_ids "$region" describe-subnets 'Subnets[].SubnetId')
      for vpc in "${vpcs[@]}"; do
        aws ec2 delete-vpc --region "$region" --vpc-id "$vpc" > /dev/null 2>&1 && echo "deleted VPC $vpc"
      done
    done
    mapfile -t vpcs < <(ec2_ids "$region" describe-vpcs 'Vpcs[].VpcId')
    if [ "${#vpcs[@]}" -gt 0 ]; then
      echo "failed to delete VPCs ${vpcs[*]}: something outside the run's tag may still use them"
    fi
  done
done

# 6. CloudFront deletes a distribution once it is deployed disabled, which
# takes a few minutes; the wait gives up after about half an hour.
for distribution in "${distributions[@]}"; do
  echo "deleting CloudFront distribution $distribution"
  delete_distribution "$distribution"
done

# IAM is global, and the Tagging API doesn't list its roles: the estates'
# roles are told by their ih-sbx- name and a run tag, which they all
# carry, with the instance profiles named ih-sbx- that hold them.
mapfile -t roles < <(words "$(aws iam list-roles --query "Roles[?starts_with(RoleName, 'ih-sbx-')].RoleName" --output text)")
for role in "${roles[@]}"; do
  tag=$(aws iam list-role-tags --role-name "$role" --query "Tags[?Key=='$key'].Value" --output text)
  if [ -z "$tag" ] || [ "$tag" = None ]; then
    continue
  fi
  echo "deleting IAM role $role, of run $tag"
  mapfile -t profiles < <(words "$(aws iam list-instance-profiles-for-role --role-name "$role" \
    --query "InstanceProfiles[?starts_with(InstanceProfileName, 'ih-sbx-')].InstanceProfileName" --output text)")
  for profile in "${profiles[@]}"; do
    try aws iam remove-role-from-instance-profile --instance-profile-name "$profile" --role-name "$role"
    try aws iam delete-instance-profile --instance-profile-name "$profile"
  done
  mapfile -t policies < <(words "$(aws iam list-attached-role-policies --role-name "$role" --query 'AttachedPolicies[].PolicyArn' --output text)")
  for policy in "${policies[@]}"; do
    try aws iam detach-role-policy --role-name "$role" --policy-arn "$policy"
  done
  mapfile -t inline < <(words "$(aws iam list-role-policies --role-name "$role" --query 'PolicyNames[]' --output text)")
  for policy in "${inline[@]}"; do
    try aws iam delete-role-policy --role-name "$role" --policy-name "$policy"
  done
  try aws iam delete-role --role-name "$role"
done

# What still carries this run's tag, or an earlier one's.
for region in "${regions[@]}"; do
  mapfile -t swept < <({ echo "$own"; runs "$region"; } | sed '/^$/d' | sort -u)
  for run in "${swept[@]}"; do
    left=$(tagged "$region" "$run" | tr '\n' ' ')
    if [ -n "$left" ]; then
      echo "::warning::Still tagged $key=$run in $region after the sweep (deletes may still be running, and the Tagging API lists deleted resources for a while): $left"
    fi
  done
done
