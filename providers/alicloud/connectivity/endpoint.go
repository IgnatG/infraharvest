package connectivity

import (
	"encoding/xml"
	"fmt"
	"os"
	"strings"
)

// Load endpoints from endpoints.xml or environment variables to meet specified application scenario, like private cloud.
type ServiceCode string

// Only the services infraharvest lists have a code.
const (
	ECSCode  = ServiceCode("ECS")
	RAMCode  = ServiceCode("RAM")
	VPCCode  = ServiceCode("VPC")
	SLBCode  = ServiceCode("SLB")
	RDSCode  = ServiceCode("RDS")
	DNSCode  = ServiceCode("DNS")
	PVTZCode = ServiceCode("PVTZ")
)

// xml
type Endpoints struct {
	Endpoint []Endpoint `xml:"Endpoint"`
}

type Endpoint struct {
	Name      string    `xml:"name,attr"`
	RegionIds RegionIds `xml:"RegionIds"`
	Products  Products  `xml:"Products"`
}

type RegionIds struct {
	RegionID string `xml:"RegionId"`
}

type Products struct {
	Product []Product `xml:"Product"`
}

type Product struct {
	ProductName string `xml:"ProductName"`
	DomainName  string `xml:"DomainName"`
}

func loadEndpoint(region string, serviceCode ServiceCode) string {
	endpoint := strings.TrimSpace(os.Getenv(fmt.Sprintf("%s_ENDPOINT", string(serviceCode))))
	if endpoint != "" {
		return endpoint
	}

	// Load current path endpoint file endpoints.xml, if failed, it will load from environment variables TF_ENDPOINT_PATH
	data, err := os.ReadFile("./endpoints.xml")
	if err != nil || len(data) == 0 {
		d, e := os.ReadFile(os.Getenv("TF_ENDPOINT_PATH"))
		if e != nil {
			return ""
		}
		data = d
	}
	var endpoints Endpoints
	err = xml.Unmarshal(data, &endpoints)
	if err != nil {
		return ""
	}
	for _, endpoint := range endpoints.Endpoint {
		if endpoint.RegionIds.RegionID == region {
			for _, product := range endpoint.Products.Product {
				if strings.EqualFold(product.ProductName, string(serviceCode)) {
					return strings.TrimSpace(product.DomainName)
				}
			}
		}
	}

	return ""
}
