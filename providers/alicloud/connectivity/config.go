package connectivity

import (
	"fmt"
	"log"

	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/auth"
	"github.com/aliyun/alibaba-cloud-sdk-go/sdk/auth/credentials"
)

// Config of aliyun
type Config struct {
	AccessKey                string
	SecretKey                string
	EcsRoleName              string
	Region                   Region
	RegionID                 string
	SecurityToken            string
	OtsInstanceName          string
	AccountID                string
	RAMRoleArn               string
	RAMRoleSessionName       string
	RAMRolePolicy            string
	RAMRoleSessionExpiration int
	EcsEndpoint              string
	RdsEndpoint              string
	SlbEndpoint              string
	VpcEndpoint              string
	CenEndpoint              string
	EssEndpoint              string
	OssEndpoint              string
	OnsEndpoint              string
	AlikafkaEndpoint         string
	DNSEndpoint              string
	RAMEndpoint              string
	CsEndpoint               string
	CrEndpoint               string
	CdnEndpoint              string
	KmsEndpoint              string
	OtsEndpoint              string
	CmsEndpoint              string
	PvtzEndpoint             string
	StsEndpoint              string
	LogEndpoint              string
	DrdsEndpoint             string
	DdsEndpoint              string
	GpdbEnpoint              string
	KVStoreEndpoint          string
	FcEndpoint               string
	ApigatewayEndpoint       string
	DatahubEndpoint          string
	MnsEndpoint              string
	LocationEndpoint         string
	ElasticsearchEndpoint    string
	NasEndpoint              string
	ActionTrailEndpoint      string
	BssOpenAPIEndpoint       string
	DdoscooEndpoint          string
	DdosbgpEndpoint          string

	SkipRegionValidation bool
	ConfigurationSource  string
}

func (c *Config) loadAndValidate() error {
	err := c.validateRegion()
	if err != nil {
		return err
	}

	return nil
}

func (c *Config) validateRegion() error {
	for _, valid := range ValidRegions {
		if c.Region == valid {
			return nil
		}
	}

	return fmt.Errorf("invalid Alibaba Cloud region: %s", c.RegionID)
}

func (c *Config) getAuthCredential() auth.Credential {
	if c.AccessKey != "" && c.SecretKey != "" {
		if c.SecurityToken != "" {
			return credentials.NewStsTokenCredential(c.AccessKey, c.SecretKey, c.SecurityToken)
		}
		if c.RAMRoleArn != "" {
			log.Printf("[INFO] Assume RAM Role specified in provider block assume_role { ... }")
			return credentials.NewRamRoleArnWithPolicyCredential(
				c.AccessKey, c.SecretKey, c.RAMRoleArn,
				c.RAMRoleSessionName, c.RAMRolePolicy, c.RAMRoleSessionExpiration)
		}
		return credentials.NewAccessKeyCredential(c.AccessKey, c.SecretKey)
	}
	if c.EcsRoleName != "" {
		return credentials.NewEcsRamRoleCredential(c.EcsRoleName)
	}

	return credentials.NewAccessKeyCredential(c.AccessKey, c.SecretKey)
}
