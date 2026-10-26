// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package cfn

// typeEntry is what the default mapper knows about a CloudFormation type.
// Properties and attributes it doesn't list are derived from their names,
// as for a type it doesn't know.
type typeEntry struct {
	// tf is the AWS provider's resource type.
	tf string
	// ref is the attribute Ref returns; empty means id.
	ref string
	// attrs maps Fn::GetAtt attributes to Terraform attributes.
	attrs map[string]string
	// props maps property paths, dot-joined (SecurityGroupIngress.CidrIp),
	// to what they become.
	props map[string]Property
}

// arg, blockProp, listArg, jsonArg, mapArg and omit build table entries.
func arg(name string) Property       { return Property{Name: name} }
func blockProp(name string) Property { return Property{Name: name, Kind: KindBlock} }
func listArg(name string) Property   { return Property{Name: name, List: true} }
func jsonArg(name string) Property   { return Property{Name: name, Kind: KindJSON} }
func mapArg(name string) Property    { return Property{Name: name, Kind: KindMap} }
func omit(reason string) Property    { return Property{Kind: KindOmit, Reason: reason} }

var tagsProp = Property{Name: "tags", Kind: KindTags}

// s3Split is the reason the S3 bucket's configuration properties are left
// out: since version 4 the AWS provider manages each with a resource of
// its own.
func s3Split(resource string) Property {
	return omit("the AWS provider manages it with a separate " + resource + " resource")
}

// securityGroupRule maps the properties of a security group's inline
// ingress or egress rule; peer is the property naming the other group.
func securityGroupRule(prefix, peer string) map[string]Property {
	return map[string]Property{
		prefix:                   blockProp(map[string]string{"SecurityGroupIngress": "ingress", "SecurityGroupEgress": "egress"}[prefix]),
		prefix + ".IpProtocol":   arg("protocol"),
		prefix + ".FromPort":     arg("from_port"),
		prefix + ".ToPort":       arg("to_port"),
		prefix + ".CidrIp":       listArg("cidr_blocks"),
		prefix + ".CidrIpv6":     listArg("ipv6_cidr_blocks"),
		prefix + ".Description":  arg("description"),
		prefix + ".PrefixListId": listArg("prefix_list_ids"),
		prefix + "." + peer:      listArg("security_groups"),
	}
}

func merge(maps ...map[string]Property) map[string]Property {
	out := map[string]Property{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// builtinTypes is the default mapper's table: types whose Terraform names
// are known from the AWS provider's documentation rather than derived.
var builtinTypes = map[string]typeEntry{
	"AWS::EC2::VPC": {
		tf: "aws_vpc",
		attrs: map[string]string{
			"CidrBlock": "cidr_block", "DefaultNetworkAcl": "default_network_acl_id",
			"DefaultSecurityGroup": "default_security_group_id", "VpcId": "id",
		},
		props: map[string]Property{
			"CidrBlock": arg("cidr_block"), "EnableDnsHostnames": arg("enable_dns_hostnames"),
			"EnableDnsSupport": arg("enable_dns_support"), "InstanceTenancy": arg("instance_tenancy"),
			"Ipv4IpamPoolId": arg("ipv4_ipam_pool_id"), "Ipv4NetmaskLength": arg("ipv4_netmask_length"),
			"Tags": tagsProp,
		},
	},
	"AWS::EC2::Subnet": {
		tf: "aws_subnet",
		attrs: map[string]string{
			"AvailabilityZone": "availability_zone", "CidrBlock": "cidr_block", "SubnetId": "id", "VpcId": "vpc_id",
		},
		props: map[string]Property{
			"AvailabilityZone": arg("availability_zone"), "AvailabilityZoneId": arg("availability_zone_id"),
			"CidrBlock": arg("cidr_block"), "Ipv6CidrBlock": arg("ipv6_cidr_block"),
			"MapPublicIpOnLaunch": arg("map_public_ip_on_launch"), "VpcId": arg("vpc_id"), "Tags": tagsProp,
		},
	},
	"AWS::EC2::InternetGateway": {
		tf:    "aws_internet_gateway",
		attrs: map[string]string{"InternetGatewayId": "id"},
		props: map[string]Property{"Tags": tagsProp},
	},
	"AWS::EC2::VPCGatewayAttachment": {
		tf: "aws_internet_gateway_attachment",
		props: map[string]Property{
			"InternetGatewayId": arg("internet_gateway_id"), "VpcId": arg("vpc_id"),
			"VpnGatewayId": omit("a VPN gateway attaches with an aws_vpn_gateway_attachment resource instead"),
		},
	},
	"AWS::EC2::RouteTable": {
		tf:    "aws_route_table",
		attrs: map[string]string{"RouteTableId": "id"},
		props: map[string]Property{"VpcId": arg("vpc_id"), "Tags": tagsProp},
	},
	"AWS::EC2::Route": {
		tf: "aws_route",
		props: map[string]Property{
			"RouteTableId": arg("route_table_id"), "DestinationCidrBlock": arg("destination_cidr_block"),
			"DestinationIpv6CidrBlock": arg("destination_ipv6_cidr_block"), "GatewayId": arg("gateway_id"),
			"NatGatewayId": arg("nat_gateway_id"), "TransitGatewayId": arg("transit_gateway_id"),
			"VpcPeeringConnectionId": arg("vpc_peering_connection_id"), "VpcEndpointId": arg("vpc_endpoint_id"),
		},
	},
	"AWS::EC2::SubnetRouteTableAssociation": {
		tf:    "aws_route_table_association",
		props: map[string]Property{"SubnetId": arg("subnet_id"), "RouteTableId": arg("route_table_id")},
	},
	"AWS::EC2::NatGateway": {
		tf: "aws_nat_gateway",
		props: map[string]Property{
			"AllocationId": arg("allocation_id"), "SubnetId": arg("subnet_id"),
			"ConnectivityType": arg("connectivity_type"), "Tags": tagsProp,
		},
	},
	"AWS::EC2::EIP": {
		tf: "aws_eip",
		// Ref to an Elastic IP returns its address, not its allocation ID.
		ref:   "public_ip",
		attrs: map[string]string{"AllocationId": "allocation_id", "PublicIp": "public_ip"},
		props: map[string]Property{"Domain": arg("domain"), "InstanceId": arg("instance"), "Tags": tagsProp},
	},
	"AWS::EC2::SecurityGroup": {
		tf:    "aws_security_group",
		attrs: map[string]string{"GroupId": "id", "VpcId": "vpc_id"},
		props: merge(map[string]Property{
			"GroupDescription": arg("description"), "GroupName": arg("name"), "VpcId": arg("vpc_id"), "Tags": tagsProp,
		}, securityGroupRule("SecurityGroupIngress", "SourceSecurityGroupId"),
			securityGroupRule("SecurityGroupEgress", "DestinationSecurityGroupId")),
	},
	"AWS::EC2::Instance": {
		tf: "aws_instance",
		attrs: map[string]string{
			"AvailabilityZone": "availability_zone", "InstanceId": "id", "PrivateDnsName": "private_dns",
			"PrivateIp": "private_ip", "PublicDnsName": "public_dns", "PublicIp": "public_ip",
		},
		props: map[string]Property{
			"ImageId": arg("ami"), "InstanceType": arg("instance_type"), "KeyName": arg("key_name"),
			"SubnetId": arg("subnet_id"), "SecurityGroupIds": arg("vpc_security_group_ids"),
			"IamInstanceProfile": arg("iam_instance_profile"), "AvailabilityZone": arg("availability_zone"),
			// CloudFormation takes user data base64-encoded.
			"UserData": arg("user_data_base64"), "Tags": tagsProp,
		},
	},
	"AWS::S3::Bucket": {
		tf: "aws_s3_bucket",
		attrs: map[string]string{
			"Arn": "arn", "DomainName": "bucket_domain_name", "RegionalDomainName": "bucket_regional_domain_name",
		},
		props: map[string]Property{
			"BucketName": arg("bucket"), "ObjectLockEnabled": arg("object_lock_enabled"), "Tags": tagsProp,
			"AccessControl":                  s3Split("aws_s3_bucket_acl"),
			"BucketEncryption":               s3Split("aws_s3_bucket_server_side_encryption_configuration"),
			"CorsConfiguration":              s3Split("aws_s3_bucket_cors_configuration"),
			"LifecycleConfiguration":         s3Split("aws_s3_bucket_lifecycle_configuration"),
			"LoggingConfiguration":           s3Split("aws_s3_bucket_logging"),
			"NotificationConfiguration":      s3Split("aws_s3_bucket_notification"),
			"ObjectLockConfiguration":        s3Split("aws_s3_bucket_object_lock_configuration"),
			"OwnershipControls":              s3Split("aws_s3_bucket_ownership_controls"),
			"PublicAccessBlockConfiguration": s3Split("aws_s3_bucket_public_access_block"),
			"ReplicationConfiguration":       s3Split("aws_s3_bucket_replication_configuration"),
			"VersioningConfiguration":        s3Split("aws_s3_bucket_versioning"),
			"WebsiteConfiguration":           s3Split("aws_s3_bucket_website_configuration"),
		},
	},
	"AWS::S3::BucketPolicy": {
		tf:    "aws_s3_bucket_policy",
		props: map[string]Property{"Bucket": arg("bucket"), "PolicyDocument": jsonArg("policy")},
	},
	"AWS::IAM::Role": {
		tf:    "aws_iam_role",
		ref:   "name",
		attrs: map[string]string{"Arn": "arn", "RoleId": "unique_id"},
		props: map[string]Property{
			"RoleName": arg("name"), "AssumeRolePolicyDocument": jsonArg("assume_role_policy"),
			"Description": arg("description"), "ManagedPolicyArns": arg("managed_policy_arns"),
			"MaxSessionDuration": arg("max_session_duration"), "Path": arg("path"),
			"PermissionsBoundary": arg("permissions_boundary"), "Tags": tagsProp,
			"Policies": blockProp("inline_policy"), "Policies.PolicyName": arg("name"),
			"Policies.PolicyDocument": jsonArg("policy"),
		},
	},
	"AWS::IAM::ManagedPolicy": {
		tf:    "aws_iam_policy",
		ref:   "arn",
		attrs: map[string]string{"PolicyArn": "arn"},
		props: map[string]Property{
			"ManagedPolicyName": arg("name"), "PolicyDocument": jsonArg("policy"),
			"Description": arg("description"), "Path": arg("path"),
		},
	},
	"AWS::IAM::User": {
		tf:    "aws_iam_user",
		ref:   "name",
		props: map[string]Property{"UserName": arg("name"), "Path": arg("path"), "Tags": tagsProp},
	},
	"AWS::IAM::Group": {
		tf:    "aws_iam_group",
		ref:   "name",
		props: map[string]Property{"GroupName": arg("name"), "Path": arg("path")},
	},
	"AWS::IAM::InstanceProfile": {
		tf:    "aws_iam_instance_profile",
		ref:   "name",
		props: map[string]Property{"InstanceProfileName": arg("name"), "Path": arg("path")},
	},
	"AWS::SNS::Topic": {
		tf:    "aws_sns_topic",
		ref:   "arn",
		attrs: map[string]string{"TopicArn": "arn", "TopicName": "name"},
		props: map[string]Property{
			"TopicName": arg("name"), "DisplayName": arg("display_name"), "FifoTopic": arg("fifo_topic"),
			"KmsMasterKeyId": arg("kms_master_key_id"), "Tags": tagsProp,
		},
	},
	"AWS::SQS::Queue": {
		tf:    "aws_sqs_queue",
		ref:   "url",
		attrs: map[string]string{"Arn": "arn", "QueueName": "name", "QueueUrl": "url"},
		props: map[string]Property{
			"QueueName": arg("name"), "DelaySeconds": arg("delay_seconds"), "FifoQueue": arg("fifo_queue"),
			"KmsMasterKeyId": arg("kms_master_key_id"), "MaximumMessageSize": arg("max_message_size"),
			"MessageRetentionPeriod":        arg("message_retention_seconds"),
			"ReceiveMessageWaitTimeSeconds": arg("receive_wait_time_seconds"),
			"RedrivePolicy":                 jsonArg("redrive_policy"),
			"VisibilityTimeout":             arg("visibility_timeout_seconds"), "Tags": tagsProp,
		},
	},
	"AWS::Lambda::Function": {
		tf:    "aws_lambda_function",
		ref:   "function_name",
		attrs: map[string]string{"Arn": "arn"},
		props: map[string]Property{
			"FunctionName": arg("function_name"), "Handler": arg("handler"), "Runtime": arg("runtime"),
			"Role": arg("role"), "MemorySize": arg("memory_size"), "Timeout": arg("timeout"),
			"Description": arg("description"), "Architectures": arg("architectures"), "Layers": arg("layers"),
			"Environment": blockProp("environment"), "Environment.Variables": mapArg("variables"),
			"Code": omit("the AWS provider takes the code as the filename, s3_bucket and s3_key, or image_uri arguments"),
			"Tags": tagsProp,
		},
	},
	"AWS::Lambda::Permission": {
		tf: "aws_lambda_permission",
		props: map[string]Property{
			"Action": arg("action"), "FunctionName": arg("function_name"), "Principal": arg("principal"),
			"SourceArn": arg("source_arn"), "SourceAccount": arg("source_account"),
		},
	},
	"AWS::Logs::LogGroup": {
		tf:    "aws_cloudwatch_log_group",
		ref:   "name",
		attrs: map[string]string{"Arn": "arn"},
		props: map[string]Property{
			"LogGroupName": arg("name"), "RetentionInDays": arg("retention_in_days"),
			"KmsKeyId": arg("kms_key_id"), "Tags": tagsProp,
		},
	},
	"AWS::KMS::Key": {
		tf:    "aws_kms_key",
		ref:   "key_id",
		attrs: map[string]string{"Arn": "arn", "KeyId": "key_id"},
		props: map[string]Property{
			"Description": arg("description"), "KeyPolicy": jsonArg("policy"),
			"EnableKeyRotation": arg("enable_key_rotation"), "KeyUsage": arg("key_usage"),
			"KeySpec": arg("customer_master_key_spec"), "MultiRegion": arg("multi_region"),
			"PendingWindowInDays": arg("deletion_window_in_days"), "Tags": tagsProp,
		},
	},
	"AWS::KMS::Alias": {
		tf:    "aws_kms_alias",
		ref:   "name",
		props: map[string]Property{"AliasName": arg("name"), "TargetKeyId": arg("target_key_id")},
	},
	"AWS::DynamoDB::Table": {
		tf:    "aws_dynamodb_table",
		ref:   "name",
		attrs: map[string]string{"Arn": "arn", "StreamArn": "stream_arn"},
		props: map[string]Property{"TableName": arg("name"), "BillingMode": arg("billing_mode"), "Tags": tagsProp},
	},
	"AWS::SSM::Parameter": {
		tf:  "aws_ssm_parameter",
		ref: "name",
		props: map[string]Property{
			"Name": arg("name"), "Type": arg("type"), "Value": arg("value"), "Description": arg("description"),
			"AllowedPattern": arg("allowed_pattern"), "Tier": arg("tier"), "DataType": arg("data_type"),
			// An SSM parameter's tags are a mapping, not a list.
			"Tags": tagsProp,
		},
	},
	"AWS::SecretsManager::Secret": {
		tf:  "aws_secretsmanager_secret",
		ref: "arn",
		props: map[string]Property{
			"Name": arg("name"), "Description": arg("description"), "KmsKeyId": arg("kms_key_id"), "Tags": tagsProp,
		},
	},
	"AWS::ECR::Repository": {
		tf:    "aws_ecr_repository",
		ref:   "name",
		attrs: map[string]string{"Arn": "arn", "RepositoryUri": "repository_url"},
		props: map[string]Property{
			"RepositoryName": arg("name"), "ImageTagMutability": arg("image_tag_mutability"), "Tags": tagsProp,
		},
	},
	"AWS::ECS::Cluster": {
		tf:    "aws_ecs_cluster",
		ref:   "name",
		attrs: map[string]string{"Arn": "arn"},
		props: map[string]Property{"ClusterName": arg("name"), "Tags": tagsProp},
	},
	"AWS::Route53::HostedZone": {
		tf:    "aws_route53_zone",
		ref:   "zone_id",
		attrs: map[string]string{"Id": "zone_id", "NameServers": "name_servers"},
		props: map[string]Property{"Name": arg("name")},
	},
	"AWS::Route53::RecordSet": {
		tf: "aws_route53_record",
		props: map[string]Property{
			"HostedZoneId": arg("zone_id"), "Name": arg("name"), "Type": arg("type"),
			"TTL": arg("ttl"), "ResourceRecords": arg("records"), "SetIdentifier": arg("set_identifier"),
		},
	},
	"AWS::ElasticLoadBalancingV2::LoadBalancer": {
		tf:  "aws_lb",
		ref: "arn",
		attrs: map[string]string{
			"CanonicalHostedZoneID": "zone_id", "DNSName": "dns_name", "LoadBalancerArn": "arn",
			"LoadBalancerName": "name",
		},
		props: map[string]Property{
			"Name": arg("name"), "Type": arg("load_balancer_type"), "Subnets": arg("subnets"),
			"SecurityGroups": arg("security_groups"), "IpAddressType": arg("ip_address_type"), "Tags": tagsProp,
		},
	},
	"AWS::ElasticLoadBalancingV2::TargetGroup": {
		tf:    "aws_lb_target_group",
		ref:   "arn",
		attrs: map[string]string{"TargetGroupArn": "arn", "TargetGroupName": "name"},
		props: map[string]Property{
			"Name": arg("name"), "Port": arg("port"), "Protocol": arg("protocol"), "VpcId": arg("vpc_id"),
			"TargetType": arg("target_type"), "Tags": tagsProp,
		},
	},
	"AWS::RDS::DBInstance": {
		tf:  "aws_db_instance",
		ref: "identifier",
		attrs: map[string]string{
			"DBInstanceArn": "arn", "Endpoint.Address": "address", "Endpoint.Port": "port",
		},
		props: map[string]Property{
			"DBInstanceIdentifier": arg("identifier"), "DBInstanceClass": arg("instance_class"),
			"Engine": arg("engine"), "EngineVersion": arg("engine_version"),
			"AllocatedStorage": arg("allocated_storage"), "StorageType": arg("storage_type"),
			"MasterUsername": arg("username"), "MasterUserPassword": arg("password"), "DBName": arg("db_name"),
			"MultiAZ": arg("multi_az"), "DBSubnetGroupName": arg("db_subnet_group_name"),
			"VPCSecurityGroups": arg("vpc_security_group_ids"), "StorageEncrypted": arg("storage_encrypted"),
			"KmsKeyId": arg("kms_key_id"), "Tags": tagsProp,
		},
	},
	"AWS::RDS::DBSubnetGroup": {
		tf:  "aws_db_subnet_group",
		ref: "name",
		props: map[string]Property{
			"DBSubnetGroupName": arg("name"), "DBSubnetGroupDescription": arg("description"),
			"SubnetIds": arg("subnet_ids"), "Tags": tagsProp,
		},
	},
	"AWS::Events::Rule": {
		tf:    "aws_cloudwatch_event_rule",
		ref:   "name",
		attrs: map[string]string{"Arn": "arn"},
		props: map[string]Property{
			"Name": arg("name"), "Description": arg("description"), "EventBusName": arg("event_bus_name"),
			"EventPattern": jsonArg("event_pattern"), "ScheduleExpression": arg("schedule_expression"),
			"State":   arg("state"),
			"Targets": omit("the AWS provider manages each target with an aws_cloudwatch_event_target resource"),
		},
	},
}
