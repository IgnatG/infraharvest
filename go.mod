module github.com/IgnatG/infraharvest

go 1.26.0

toolchain go1.27.2

require (
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/logging v1.13.1
	cloud.google.com/go/storage v1.61.3
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.23.2
	github.com/Azure/azure-sdk-for-go/sdk/azidentity v1.14.1
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/analysisservices/armanalysisservices v1.2.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v6 v6.0.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v8 v8.4.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerinstance/armcontainerinstance/v2 v2.4.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry/v3 v3.0.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cosmos/armcosmos/v4 v4.0.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/databricks/armdatabricks/v2 v2.0.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/datafactory/armdatafactory/v11 v11.0.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns v1.2.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/eventhub/armeventhub v1.3.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault/v2 v2.0.2
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/mariadb/armmariadb v1.2.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/mysql/armmysql v1.2.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v11 v11.0.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresql v1.2.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns/v2 v2.0.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/purview/armpurview v1.2.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v4 v4.0.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armlocks v1.2.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v4 v4.0.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/security/armsecurity v0.15.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/sql/armsql v1.2.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage/v4 v4.2.0
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/synapse/armsynapse v0.8.0
	github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v1.8.2
	github.com/IBM-Cloud/bluemix-go v0.0.0-20260916083538-6e91b9f8f36f
	github.com/IBM/go-sdk-core/v5 v5.23.4
	github.com/IBM/ibm-cos-sdk-go v1.5.0
	github.com/IBM/keyprotect-go-client v0.8.1
	github.com/IBM/networking-go-sdk v0.30.0
	github.com/IBM/platform-services-go-sdk v0.26.1
	github.com/IBM/vpc-go-sdk v0.64.0
	github.com/OctopusDeploy/go-octopusdeploy v1.6.0
	github.com/PaloAltoNetworks/pango v0.8.0
	github.com/aliyun/alibaba-cloud-sdk-go v1.61.1247
	github.com/aliyun/aliyun-tablestore-go-sdk v4.1.2+incompatible
	github.com/apache/openwhisk-client-go v0.0.0-20210106144548-17d556327cd3
	github.com/aws/aws-sdk-go-v2 v1.47.1
	github.com/aws/aws-sdk-go-v2/config v1.32.12
	github.com/aws/aws-sdk-go-v2/credentials v1.19.12
	github.com/aws/aws-sdk-go-v2/service/accessanalyzer v1.26.5
	github.com/aws/aws-sdk-go-v2/service/acm v1.22.5
	github.com/aws/aws-sdk-go-v2/service/apigateway v1.21.5
	github.com/aws/aws-sdk-go-v2/service/apigatewayv2 v1.44.0
	github.com/aws/aws-sdk-go-v2/service/appsync v1.26.5
	github.com/aws/aws-sdk-go-v2/service/autoscaling v1.36.5
	github.com/aws/aws-sdk-go-v2/service/batch v1.30.5
	github.com/aws/aws-sdk-go-v2/service/budgets v1.20.5
	github.com/aws/aws-sdk-go-v2/service/cloud9 v1.22.3
	github.com/aws/aws-sdk-go-v2/service/cloudformation v1.42.4
	github.com/aws/aws-sdk-go-v2/service/cloudfront v1.32.5
	github.com/aws/aws-sdk-go-v2/service/cloudhsmv2 v1.19.5
	github.com/aws/aws-sdk-go-v2/service/cloudtrail v1.35.5
	github.com/aws/aws-sdk-go-v2/service/cloudwatch v1.32.0
	github.com/aws/aws-sdk-go-v2/service/cloudwatchevents v1.21.5
	github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs v1.65.0
	github.com/aws/aws-sdk-go-v2/service/codebuild v1.26.5
	github.com/aws/aws-sdk-go-v2/service/codecommit v1.19.5
	github.com/aws/aws-sdk-go-v2/service/codedeploy v1.22.1
	github.com/aws/aws-sdk-go-v2/service/codepipeline v1.22.5
	github.com/aws/aws-sdk-go-v2/service/cognitoidentity v1.21.5
	github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider v1.31.5
	github.com/aws/aws-sdk-go-v2/service/configservice v1.43.5
	github.com/aws/aws-sdk-go-v2/service/datapipeline v1.19.5
	github.com/aws/aws-sdk-go-v2/service/devicefarm v1.20.5
	github.com/aws/aws-sdk-go-v2/service/docdb v1.29.5
	github.com/aws/aws-sdk-go-v2/service/dynamodb v1.56.2
	github.com/aws/aws-sdk-go-v2/service/ec2 v1.141.0
	github.com/aws/aws-sdk-go-v2/service/ecr v1.24.5
	github.com/aws/aws-sdk-go-v2/service/ecrpublic v1.21.5
	github.com/aws/aws-sdk-go-v2/service/ecs v1.56.3
	github.com/aws/aws-sdk-go-v2/service/efs v1.26.5
	github.com/aws/aws-sdk-go-v2/service/eks v1.35.5
	github.com/aws/aws-sdk-go-v2/service/elasticache v1.34.5
	github.com/aws/aws-sdk-go-v2/service/elasticbeanstalk v1.20.5
	github.com/aws/aws-sdk-go-v2/service/elasticloadbalancing v1.21.5
	github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2 v1.26.5
	github.com/aws/aws-sdk-go-v2/service/elasticsearchservice v1.24.5
	github.com/aws/aws-sdk-go-v2/service/emr v1.49.0
	github.com/aws/aws-sdk-go-v2/service/firehose v1.22.5
	github.com/aws/aws-sdk-go-v2/service/glue v1.72.4
	github.com/aws/aws-sdk-go-v2/service/iam v1.53.6
	github.com/aws/aws-sdk-go-v2/service/identitystore v1.28.3
	github.com/aws/aws-sdk-go-v2/service/iot v1.46.5
	github.com/aws/aws-sdk-go-v2/service/kafka v1.28.5
	github.com/aws/aws-sdk-go-v2/service/kinesis v1.43.5
	github.com/aws/aws-sdk-go-v2/service/kms v1.27.5
	github.com/aws/aws-sdk-go-v2/service/lambda v1.88.5
	github.com/aws/aws-sdk-go-v2/service/medialive v1.43.3
	github.com/aws/aws-sdk-go-v2/service/mediapackage v1.28.5
	github.com/aws/aws-sdk-go-v2/service/mediastore v1.18.5
	github.com/aws/aws-sdk-go-v2/service/mq v1.20.5
	github.com/aws/aws-sdk-go-v2/service/opsworks v1.19.5
	github.com/aws/aws-sdk-go-v2/service/organizations v1.23.5
	github.com/aws/aws-sdk-go-v2/service/qldb v1.19.5
	github.com/aws/aws-sdk-go-v2/service/rds v1.64.5
	github.com/aws/aws-sdk-go-v2/service/redshift v1.39.6
	github.com/aws/aws-sdk-go-v2/service/resourcegroups v1.19.5
	github.com/aws/aws-sdk-go-v2/service/route53 v1.35.5
	github.com/aws/aws-sdk-go-v2/service/s3 v1.97.3
	github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.25.5
	github.com/aws/aws-sdk-go-v2/service/securityhub v1.43.5
	github.com/aws/aws-sdk-go-v2/service/servicecatalog v1.25.5
	github.com/aws/aws-sdk-go-v2/service/ses v1.19.5
	github.com/aws/aws-sdk-go-v2/service/sfn v1.24.5
	github.com/aws/aws-sdk-go-v2/service/sns v1.39.13
	github.com/aws/aws-sdk-go-v2/service/sqs v1.42.24
	github.com/aws/aws-sdk-go-v2/service/ssm v1.44.5
	github.com/aws/aws-sdk-go-v2/service/ssoadmin v1.23.5
	github.com/aws/aws-sdk-go-v2/service/sts v1.41.9
	github.com/aws/aws-sdk-go-v2/service/swf v1.20.5
	github.com/aws/aws-sdk-go-v2/service/waf v1.18.5
	github.com/aws/aws-sdk-go-v2/service/wafregional v1.19.5
	github.com/aws/aws-sdk-go-v2/service/wafv2 v1.43.5
	github.com/aws/aws-sdk-go-v2/service/workspaces v1.35.5
	github.com/aws/aws-sdk-go-v2/service/xray v1.23.5
	github.com/cenkalti/backoff v2.2.1+incompatible // indirect
	github.com/cloudflare/cloudflare-go/v7 v7.12.0
	github.com/cloudfoundry/jibber_jabber v0.0.0-20151120183258-bcc4c8345a21 // indirect
	github.com/ddelnano/terraform-provider-mikrotik/client v0.0.0-20210401060029-7f652169b2c4
	github.com/ddelnano/terraform-provider-xenorchestra/client v0.0.0-20210401070256-0d721c6762ef
	github.com/denverdino/aliyungo v0.0.0-20200327235253-d59c209c7e93
	github.com/digitalocean/godo v1.217.0
	github.com/fastly/go-fastly/v7 v7.0.0
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/google/go-github/v92 v92.0.0
	github.com/gophercloud/gophercloud v1.0.0
	github.com/grafana/grafana-api-golang-client v0.0.0-20210218192924-9ccd2365d2a6
	github.com/hashicorp/go-cleanhttp v0.5.2
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/heimweh/go-pagerduty v0.0.0-20210930203304-530eff2acdc6
	github.com/heroku/heroku-go/v5 v5.4.1
	github.com/hokaccha/go-prettyjson v0.0.0-20210113012101-fb4e108d2519 // indirect
	github.com/honeycombio/terraform-provider-honeycombio v0.10.0
	github.com/iancoleman/strcase v0.3.0
	github.com/ionos-cloud/sdk-go-dbaas-mongo v1.3.1
	github.com/ionos-cloud/sdk-go-dbaas-postgres v1.1.2
	github.com/ionos-cloud/sdk-go/v6 v6.1.3
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/jonboydell/logzio_client v1.2.0
	github.com/labd/commercetools-go-sdk v1.10.0
	github.com/linode/linodego v1.69.1
	github.com/microsoft/azure-devops-go-api/azuredevops v1.0.0-b5
	github.com/mrparkers/terraform-provider-keycloak v0.0.0-20221013232944-56f37a07590d
	github.com/nicksnyder/go-i18n v1.10.1 // indirect
	github.com/opsgenie/opsgenie-go-sdk-v2 v1.2.9
	github.com/packethost/packngo v0.30.0
	github.com/pkg/errors v0.9.1
	github.com/ryanuber/go-glob v1.0.0 // indirect
	github.com/spf13/cobra v1.1.1
	github.com/spf13/pflag v1.0.10
	github.com/tencentyun/cos-go-sdk-v5 v0.7.34
	github.com/vultr/govultr v1.1.1
	github.com/yandex-cloud/go-genproto v0.0.0-20220314102905-1acaee8ca7eb
	github.com/yandex-cloud/go-sdk v0.0.0-20220314105123-d0c2a928feb6
	github.com/zclconf/go-cty v1.18.1
	github.com/zorkian/go-datadog-api v2.30.0+incompatible
	gitlab.com/gitlab-org/api/client-go/v2 v2.64.0
	golang.org/x/oauth2 v0.36.0
	golang.org/x/text v0.42.0
	gonum.org/v1/gonum v0.17.0
	google.golang.org/api v0.271.0
	google.golang.org/genproto v0.0.0-20260128011058-8636f8732409 // indirect
	k8s.io/apimachinery v0.37.0
	k8s.io/client-go v0.37.0
)

require (
	github.com/IBM-Cloud/container-services-go-sdk v0.0.0-20250409011111-61af13302654
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.6 // indirect
	github.com/mackerelio/mackerel-client-go v0.21.0
)

require (
	github.com/antihax/optional v1.0.0 // indirect
	github.com/launchdarkly/api-client-go v5.3.0+incompatible
)

require github.com/newrelic/newrelic-client-go/v2 v2.96.1

require (
	github.com/BurntSushi/toml v1.1.0 // indirect
	github.com/agext/levenshtein v1.2.3 // indirect
	github.com/apparentlymart/go-textseg/v13 v13.0.0 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.8 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.20 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.22 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/endpoint-discovery v1.11.20 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.21 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.21 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.30.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.35.17 // indirect
	github.com/aws/smithy-go v1.28.1
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dghubble/sling v1.1.0 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/ghodss/yaml v1.0.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-openapi/errors v0.22.8 // indirect
	github.com/go-openapi/strfmt v0.27.2 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-resty/resty/v2 v2.17.2 // indirect
	github.com/go-routeros/routeros v0.0.0-20210123142807-2a44d57c6730 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/google/jsonapi v1.0.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/googleapis/gax-go/v2 v2.17.0 // indirect
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-retryablehttp v0.7.8 // indirect
	github.com/hashicorp/go-rootcerts v1.0.2 // indirect
	github.com/hashicorp/go-sockaddr v1.0.7 // indirect
	github.com/hashicorp/go-uuid v1.0.3 // indirect
	github.com/hashicorp/go-version v1.9.0
	github.com/hashicorp/hcl/v2 v2.17.0
	github.com/imdario/mergo v0.3.16 // indirect
	github.com/inconshreveable/mousetrap v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kelseyhightower/envconfig v1.4.0 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mitchellh/go-homedir v1.1.0
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/mozillazg/go-httpheader v0.2.1 // indirect
	github.com/patrickmn/go-cache v0.0.0-20180815053127-5633e0862627 // indirect
	github.com/pborman/uuid v1.2.0 // indirect
	github.com/pelletier/go-toml v1.7.0 // indirect
	github.com/peterhellberg/link v1.1.0 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/sourcegraph/jsonrpc2 v0.0.0-20210201082850-366fbb520750 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/tomnomnom/linkheader v0.0.0-20180905144013-02ca5825eb80 // indirect
	github.com/xdg-go/pbkdf2 v1.0.0 // indirect
	github.com/xdg-go/scram v1.1.2 // indirect
	github.com/xdg-go/stringprep v1.0.4 // indirect
	github.com/youmark/pkcs8 v0.0.0-20240726163527-a2c0da244d78 // indirect
	go.mongodb.org/mongo-driver v1.17.7 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/lint v0.0.0-20210508222113-6edffad5e616 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/term v0.46.0
	golang.org/x/time v0.15.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/auth0.v5 v5.21.1
	gopkg.in/go-playground/assert.v1 v1.2.1 // indirect
	gopkg.in/go-playground/validator.v9 v9.31.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/ini.v1 v1.67.2 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/api v0.37.0 // indirect
	k8s.io/klog/v2 v2.140.0 // indirect
	k8s.io/utils v0.0.0-20260626114624-be93311217bd // indirect
	sigs.k8s.io/yaml v1.6.0 // indirect
)

require github.com/PuerkitoBio/rehttp v1.0.0 // indirect

require (
	cloud.google.com/go/cloudbuild v1.25.0
	cloud.google.com/go/cloudtasks v1.13.7
	cloud.google.com/go/iam v1.5.3 // indirect
	cloud.google.com/go/monitoring v1.24.3
	github.com/DataDog/datadog-api-client-go/v2 v2.66.0
	github.com/Myra-Security-GmbH/myrasec-go/v2 v2.28.0
	github.com/bradleyfalzon/ghinstallation/v2 v2.19.0
	github.com/manicminer/hamilton v0.44.0
	github.com/opalsecurity/opal-go v1.0.19
	gopkg.in/ns1/ns1-go.v2 v2.6.5
)

require (
	cel.dev/expr v0.25.3 // indirect
	cloud.google.com/go/auth v0.18.2 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	cloud.google.com/go/longrunning v0.8.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.12.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/alertsmanagement/armalertsmanagement v0.10.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/apimanagement/armapimanagement/v3 v3.0.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3 v3.1.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/applicationinsights/armapplicationinsights v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appplatform/armappplatform/v2 v2.0.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v4 v4.1.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/automation/armautomation v0.9.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/botservice/armbotservice v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cdn/armcdn/v2 v2.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/cognitiveservices/armcognitiveservices v1.8.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5 v5.7.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v9 v9.3.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/costmanagement/armcostmanagement/v2 v2.1.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/datafactory/armdatafactory/v7 v7.0.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dataprotection/armdataprotection/v3 v3.1.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/datashare/armdatashare v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/desktopvirtualization/armdesktopvirtualization/v2 v2.3.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/devtestlabs/armdevtestlabs v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/digitaltwins/armdigitaltwins v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/domainservices/armdomainservices v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/frontdoor/armfrontdoor v1.4.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/hdinsight/armhdinsight v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/hybridkubernetes/armhybridkubernetes v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/iothub/armiothub v1.3.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault v1.5.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/kusto/armkusto/v2 v2.3.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/logic/armlogic v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/machinelearning/armmachinelearning/v4 v4.0.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/netapp/armnetapp/v10 v10.0.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6 v6.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/operationalinsights/armoperationalinsights/v3 v3.0.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/oracledatabase/armoracledatabase v1.1.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/paloaltonetworksngfw/armpanngfw v1.1.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/recoveryservices/armrecoveryservicesbackup/v4 v4.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/recoveryservices/armrecoveryservicessiterecovery/v2 v2.4.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redisenterprise/armredisenterprise v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resourcegraph/armresourcegraph v0.9.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armdeploymentscripts/v2 v2.1.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v2 v2.0.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/securityinsights/armsecurityinsights/v2 v2.0.0-beta.4 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage v1.8.1 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagecache/armstoragecache/v4 v4.0.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagemover/armstoragemover/v2 v2.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storagepool/armstoragepool v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/streamanalytics/armstreamanalytics v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/timeseriesinsights/armtimeseriesinsights v1.2.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/webpubsub/armwebpubsub v1.3.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/workloads/armworkloads v1.1.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/storage/internal v0.1.0 // indirect
	github.com/AzureAD/microsoft-authentication-library-for-go v1.8.0 // indirect
	github.com/DataDog/zstd v1.5.2 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/detectors/gcp v1.33.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric v0.55.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/internal/resourcemapping v0.55.0 // indirect
	github.com/Myra-Security-GmbH/signature v1.0.0 // indirect
	github.com/ProtonMail/go-crypto v1.5.2 // indirect
	github.com/apache/arrow-go/v18 v18.7.0 // indirect
	github.com/apparentlymart/go-textseg/v15 v15.0.0 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.0.8 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260811164956-006e29f97886 // indirect
	github.com/charmbracelet/x/ansi v0.11.8 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clbanning/mxj v1.8.4 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/cncf/xds/go v0.0.0-20260202195803-dba9d589def2 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.2.0 // indirect
	github.com/emicklei/go-restful/v3 v3.13.0 // indirect
	github.com/envoyproxy/go-control-plane/envoy v1.37.0 // indirect
	github.com/envoyproxy/protoc-gen-validate v1.3.3 // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/fxamacker/cbor/v2 v2.9.1 // indirect
	github.com/gabriel-vasile/mimetype v1.4.15 // indirect
	github.com/go-jose/go-jose/v3 v3.0.5 // indirect
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-openapi/jsonpointer v1.0.0 // indirect
	github.com/go-openapi/jsonreference v1.0.0 // indirect
	github.com/go-openapi/swag v0.27.1 // indirect
	github.com/go-openapi/swag/cmdutils v0.27.1 // indirect
	github.com/go-openapi/swag/conv v0.27.1 // indirect
	github.com/go-openapi/swag/fileutils v0.27.1 // indirect
	github.com/go-openapi/swag/jsonutils v0.27.1 // indirect
	github.com/go-openapi/swag/loading v0.27.1 // indirect
	github.com/go-openapi/swag/mangling v0.27.1 // indirect
	github.com/go-openapi/swag/netutils v0.27.1 // indirect
	github.com/go-openapi/swag/pools v0.27.1 // indirect
	github.com/go-openapi/swag/stringutils v0.27.1 // indirect
	github.com/go-openapi/swag/typeutils v0.27.1 // indirect
	github.com/go-openapi/swag/yamlutils v0.27.1 // indirect
	github.com/go-playground/validator/v10 v10.30.4 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/goccy/go-json v0.10.6 // indirect
	github.com/golang-jwt/jwt/v4 v4.5.2 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/google/gnostic-models v0.7.0 // indirect
	github.com/google/go-github/v88 v88.0.0 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.14 // indirect
	github.com/hashicorp/go-secure-stdlib/parseutil v0.2.0 // indirect
	github.com/hashicorp/go-secure-stdlib/strutil v0.1.2 // indirect
	github.com/hashicorp/hcl v1.0.1-vault-7 // indirect
	github.com/hashicorp/terraform-plugin-log v0.10.0 // indirect
	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/lestrrat-go/backoff/v2 v2.0.8 // indirect
	github.com/lestrrat-go/blackmagic v1.0.2 // indirect
	github.com/lestrrat-go/httpcc v1.0.1 // indirect
	github.com/lestrrat-go/iter v1.0.2 // indirect
	github.com/lestrrat-go/jwx v1.2.29 // indirect
	github.com/lestrrat-go/option v1.0.1 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.1 // indirect
	github.com/magodo/workerpool v0.0.0-20240524082508-11838001bc35 // indirect
	github.com/mattn/go-runewidth v0.0.24 // indirect
	github.com/montanaflynn/stats v0.7.1 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/oklog/ulid/v2 v2.1.2 // indirect
	github.com/pierrec/lz4/v4 v4.1.28 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	github.com/planetscale/vtprotobuf v0.6.1-0.20240319094008-0393e58bdf10 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/robertkrimen/otto v0.5.1 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/spiffe/go-spiffe/v2 v2.7.0 // indirect
	github.com/tidwall/gjson v1.14.4 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.1 // indirect
	github.com/tidwall/sjson v1.2.5 // indirect
	github.com/valyala/fastjson v1.6.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/detectors/gcp v1.44.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.63.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.61.0 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/sdk v1.45.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/exp v0.0.0-20260813180055-c1d0aacb2297 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/tools/cmd/cover v0.1.0-deprecated // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	gopkg.in/evanphx/json-patch.v4 v4.13.0 // indirect
	gopkg.in/sourcemap.v1 v1.0.5 // indirect
	k8s.io/kube-openapi v0.0.0-20260721132016-d427ff9ee9ad // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.2 // indirect
)

require (
	charm.land/bubbletea/v2 v2.0.10
	charm.land/lipgloss/v2 v2.0.6
	github.com/IBM/continuous-delivery-go-sdk/v2 v2.0.2
	github.com/aws/aws-sdk-go-v2/service/cloudcontrol v1.38.1
	github.com/aws/aws-sdk-go-v2/service/directconnect v1.30.1
	github.com/gofrs/uuid/v3 v3.1.2
	github.com/hashicorp/hc-install v0.10.0
	github.com/hashicorp/terraform-exec v0.25.3
	github.com/hashicorp/terraform-json v0.28.0
	github.com/hashicorp/vault/api v1.23.0
	github.com/ionos-cloud/sdk-go-cert-manager v1.0.0
	github.com/ionos-cloud/sdk-go-container-registry v1.0.0
	github.com/ionos-cloud/sdk-go-dataplatform v1.0.1
	github.com/ionos-cloud/sdk-go-dns v1.1.1
	github.com/ionos-cloud/sdk-go-logging v1.0.1
	github.com/magodo/armid v0.0.0-20240524082432-7ce06ae46c33
	github.com/magodo/azlist v0.0.0-20250827005956-f76aa26d5fe5
	github.com/magodo/aztft v0.3.1-0.20261003023401-cf187e4aed82
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.com/okta/okta-sdk-golang/v5 v5.0.1
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/as v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cbs v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cdb v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cdn v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cfs v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/clb v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common v1.0.694
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cvm v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/dnspod v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/es v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/gaap v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/mongodb v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/pts v1.0.694
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/redis v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/scf v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ses v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/ssl v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tat v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/tcaplusdb v1.0.392
	github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/vpc v1.0.392
	go.yaml.in/yaml/v3 v3.0.5
)

replace gopkg.in/jarcoal/httpmock.v1 => github.com/jarcoal/httpmock v1.0.5

replace gopkg.in/ns1/ns1-go.v2 => github.com/ns1/ns1-go/v2 v2.6.5

replace github.com/tencentcloud/tencentcloud-sdk-go => github.com/tencentcloud/tencentcloud-sdk-go v1.0.392
