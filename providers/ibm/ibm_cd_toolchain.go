package ibm

import (
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/IBM-Cloud/bluemix-go"
	"github.com/IBM-Cloud/bluemix-go/api/resource/resourcev1/catalog"
	"github.com/IBM-Cloud/bluemix-go/api/resource/resourcev2/controllerv2"
	"github.com/IBM-Cloud/bluemix-go/session"
	"github.com/IBM/continuous-delivery-go-sdk/v2/cdtektonpipelinev2"
	"github.com/IBM/continuous-delivery-go-sdk/v2/cdtoolchainv2"
	"github.com/IBM/go-sdk-core/v5/core"
	"github.com/IBM/platform-services-go-sdk/iampolicymanagementv1"
	"github.com/IgnatG/infraharvest/terraformutils"
)

type ToolchainGenerator struct {
	IBMService
}

func (g ToolchainGenerator) loadToolchain(tcID string, tcName string) terraformutils.Resource {
	resource := terraformutils.NewSimpleResource(
		tcID,
		tcName,
		"ibm_cd_toolchain",
		"ibm")

	return resource
}

func (g ToolchainGenerator) loadTool(resourceType string, tID string, tName string) terraformutils.Resource {
	resource := terraformutils.NewResource(
		tID,
		tName,
		resourceType,
		"ibm",
		map[string]string{})

	return resource
}

// Adds S2S authorization required by some integrations
func (g ToolchainGenerator) loadAuthPolicies(policyID string) terraformutils.Resource {
	resource := terraformutils.NewResource(
		policyID,
		normalizeResourceName("iam_authorization_policy", true),
		"ibm_iam_authorization_policy",
		"ibm",
		map[string]string{})

	return resource
}

func (g ToolchainGenerator) loadPL(plID string, plName string) terraformutils.Resource {
	resource := terraformutils.NewResource(
		plID,
		plName,
		"ibm_cd_tekton_pipeline",
		"ibm",
		map[string]string{})

	return resource
}

func (g ToolchainGenerator) loadPLProp(resourceType string, pID string, pName string) terraformutils.Resource {
	resource := terraformutils.NewResource(
		pID,
		pName,
		resourceType,
		"ibm",
		map[string]string{})

	return resource
}

func (g ToolchainGenerator) loadPLDef(resourceType string, pID string, pName string) terraformutils.Resource {
	resource := terraformutils.NewResource(
		pID,
		pName,
		resourceType,
		"ibm",
		map[string]string{})

	return resource
}

func (g ToolchainGenerator) loadPLTrigProp(resourceType string, pID string, pName string) terraformutils.Resource {
	resource := terraformutils.NewResource(
		pID,
		pName,
		resourceType,
		"ibm",
		map[string]string{})

	return resource
}

// Goroutine helper to handle different tool types
func (g *ToolchainGenerator) HandleTool(t cdtoolchainv2.ToolModel, toolType string, tID string, tName string, waitGroup *sync.WaitGroup) error {
	defer waitGroup.Done()

	apiKey := os.Getenv("IC_API_KEY")

	// typical case. handle exceptional cases seperately
	// maps tool_type_id to the terraform resource type
	supportedTools := map[string]string{
		"appconfig":           "ibm_cd_toolchain_tool_appconfig",
		"artifactory":         "ibm_cd_toolchain_tool_artifactory",
		"bitbucketgit":        "ibm_cd_toolchain_tool_bitbucketgit",
		"private_worker":      "ibm_cd_toolchain_tool_privateworker",
		"draservicebroker":    "ibm_cd_toolchain_tool_devopsinsights",
		"eventnotifications":  "ibm_cd_toolchain_tool_eventnotifications",
		"hostedgit":           "ibm_cd_toolchain_tool_hostedgit",
		"githubconsolidated":  "ibm_cd_toolchain_tool_githubconsolidated",
		"gitlab":              "ibm_cd_toolchain_tool_gitlab",
		"hashicorpvault":      "ibm_cd_toolchain_tool_hashicorpvault",
		"jenkins":             "ibm_cd_toolchain_tool_jenkins",
		"jira":                "ibm_cd_toolchain_tool_jira",
		"keyprotect":          "ibm_cd_toolchain_tool_keyprotect",
		"nexus":               "ibm_cd_toolchain_tool_nexus",
		"customtool":          "ibm_cd_toolchain_tool_custom",
		"saucelabs":           "ibm_cd_toolchain_tool_saucelabs",
		"secretsmanager":      "ibm_cd_toolchain_tool_secretsmanager",
		"security_compliance": "ibm_cd_toolchain_tool_securitycompliance",
		"slack":               "ibm_cd_toolchain_tool_slack",
		"sonarqube":           "ibm_cd_toolchain_tool_sonarqube",
	}

	if resourceType, ok := supportedTools[toolType]; ok {
		resourceMutex.Lock()
		g.Resources = append(g.Resources, g.loadTool(resourceType, tID, tName))
		resourceMutex.Unlock()
	} else {
		switch toolType {
		case "pipeline":
			// Classic pipelines cannot be created using Terraform
			if t.Parameters["type"] != "tekton" {
				resourceMutex.Lock()
				g.Resources = append(g.Resources, g.loadTool("ibm_cd_toolchain_tool_pipeline", tID, tName+"--classic"))
				resourceMutex.Unlock()
				fmt.Println("......! Only Tekton pipelines are supported in Terraform", toolType)
				return nil
			}

			resourceMutex.Lock()
			g.Resources = append(g.Resources, g.loadTool("ibm_cd_toolchain_tool_pipeline", tID, tName+"--tekton"))
			resourceMutex.Unlock()

			plID := *(t.ID)
			plName := tName

			resourceMutex.Lock()
			g.Resources = append(g.Resources, g.loadPL(plID, plName))
			resourceMutex.Unlock()

			// Get pipeline
			cdTektonPipelineServiceOptions := &cdtektonpipelinev2.CdTektonPipelineV2Options{
				Authenticator: &core.IamAuthenticator{
					ApiKey: apiKey,
				},
			}

			cdTektonPipelineService, err := cdtektonpipelinev2.NewCdTektonPipelineV2UsingExternalConfig(cdTektonPipelineServiceOptions)
			if err != nil {
				log.Print("......! Error getting pipeline information: ", err)
			}

			getTektonPipelineOptions := cdTektonPipelineService.NewGetTektonPipelineOptions(plID)

			tektonPipeline, _, err := cdTektonPipelineService.GetTektonPipeline(getTektonPipelineOptions)
			if err != nil {
				log.Print("......! Error getting pipeline information: ", err)
			}

			// Definitions
			for _, def := range tektonPipeline.Definitions {
				defID := fmt.Sprintf("%s/%s", plID, *(def.ID))
				defName := normalizeResourceName("definition", true)

				resourceMutex.Lock()
				g.Resources = append(g.Resources, g.loadPLDef("ibm_cd_tekton_pipeline_definition", defID, defName))
				resourceMutex.Unlock()
			}

			// Properties
			for _, prop := range tektonPipeline.Properties {
				pID := fmt.Sprintf("%s/%s", plID, *(prop.Name))
				pName := normalizeResourceName(*(prop.Name), true)

				resourceMutex.Lock()
				g.Resources = append(g.Resources, g.loadPLProp("ibm_cd_tekton_pipeline_property", pID, pName))
				resourceMutex.Unlock()
			}

			// Triggers
			for _, trig := range tektonPipeline.Triggers {
				trigger := trig.(*cdtektonpipelinev2.Trigger)

				trigID := fmt.Sprintf("%s/%s", plID, *(trigger.ID))
				trigName := normalizeResourceName(*(trigger.Name), true)

				resourceMutex.Lock()
				g.Resources = append(g.Resources, g.loadPLProp("ibm_cd_tekton_pipeline_trigger", trigID, trigName))
				resourceMutex.Unlock()

				// Trigger Properties
				for _, trigp := range trigger.Properties {
					trigpID := fmt.Sprintf("%s/%s", trigID, *(trigp.Name))
					trigpName := normalizeResourceName(*(trigp.Name), true)

					resourceMutex.Lock()
					g.Resources = append(g.Resources, g.loadPLTrigProp("ibm_cd_tekton_pipeline_trigger_property", trigpID, trigpName))
					resourceMutex.Unlock()
				}
			}
		case "pagerduty":
			// If this integration is misconfigured, it lacks the necessary fields to work in Terraform
			if *(t.State) == "configured" {
				resourceMutex.Lock()
				g.Resources = append(g.Resources, g.loadTool("ibm_cd_toolchain_tool_pagerduty", tID, tName))
				resourceMutex.Unlock()
			}
		default:
			fmt.Println("......! Unknown tool type", toolType)
		}
	}
	return nil
}

// Called within InitResources when IBM_CD_TOOLCHAIN_INCLUDE_S2S is set
func getS2SPolicies(sess *session.Session, targetTcID string) (map[string][]iampolicymanagementv1.Policy, error) {
	apiKey := os.Getenv("IC_API_KEY")

	emptyPolicies := map[string][]iampolicymanagementv1.Policy{}

	iamPolicyOptions := &iampolicymanagementv1.IamPolicyManagementV1Options{
		URL: "https://iam.cloud.ibm.com",
		Authenticator: &core.IamAuthenticator{
			ApiKey: apiKey,
		},
	}

	iamPolicyClient, err := iampolicymanagementv1.NewIamPolicyManagementV1(iamPolicyOptions)
	if err != nil {
		return emptyPolicies, err
	}

	userInfo, err := fetchUserDetails(sess, 2)
	if err != nil {
		return emptyPolicies, err
	}
	accountID := userInfo.userAccount

	listAuthPolicyOptions := iampolicymanagementv1.ListPoliciesOptions{
		AccountID: core.StringPtr(accountID),
		Type:      core.StringPtr("authorization"),
	}

	authPolicyList, _, err := iamPolicyClient.ListPolicies(&listAuthPolicyOptions)
	if err != nil {
		return emptyPolicies, fmt.Errorf("error retrieving authorization policy: %s", err)
	}
	authPolicies := authPolicyList.Policies

	s2sPolicies := map[string][]iampolicymanagementv1.Policy{} // map of toolchain id to s2s policies under it

	for _, ap := range authPolicies {
		for _, a := range ap.Subjects[0].Attributes {
			if *(a.Name) != "serviceInstance" {
				continue
			}
			if (targetTcID != "" && *(a.Value) == targetTcID) || targetTcID == "" {
				// get s2s policies for target toolchain
				if _, ok := s2sPolicies[*(a.Value)]; !ok {
					s2sPolicies[*(a.Value)] = []iampolicymanagementv1.Policy{}
				}
				s2sPolicies[*(a.Value)] = append(s2sPolicies[*(a.Value)], ap)
			}
		}
	}
	return s2sPolicies, nil
}

func (g *ToolchainGenerator) InitResources() error {
	region := g.Args["region"].(string)

	guidRegex := regexp.MustCompile("[0-9a-fA-F]{8}-([0-9a-fA-F]{4}-){3}[0-9a-fA-F]{12}$")

	targetTcID := os.Getenv("IBM_CD_TOOLCHAIN_TARGET")
	if targetTcID != "" && !guidRegex.MatchString(targetTcID) {
		log.Fatal("Env variable IBM_CD_TOOLCHAIN_TARGET is not a GUID")
	}

	apiKey := os.Getenv("IC_API_KEY")
	if apiKey == "" {
		log.Fatal("No API key set")
	}

	bmxConfig := &bluemix.Config{
		BluemixAPIKey: apiKey,
		Region:        region,
	}

	sess, err := session.New(bmxConfig)
	if err != nil {
		return err
	}

	err = authenticateAPIKey(sess)
	if err != nil {
		return err
	}

	catalogClient, err := catalog.New(sess)
	if err != nil {
		return err
	}

	controllerClient, err := controllerv2.New(sess)
	if err != nil {
		return err
	}

	serviceID, err := catalogClient.ResourceCatalog().FindByName("toolchain", true)
	if err != nil {
		return err
	}

	query := controllerv2.ServiceInstanceQuery{
		ServiceID: serviceID[0].ID,
	}

	tcInstances, err := controllerClient.ResourceServiceInstanceV2().ListInstances(query)
	if err != nil {
		return err
	}

	// Get s2s policies
	s2sPolicies := map[string][]iampolicymanagementv1.Policy{}

	includeS2S := os.Getenv("IBM_CD_TOOLCHAIN_INCLUDE_S2S")
	if includeS2S != "" {
		s2sPolicies, err = getS2SPolicies(sess, targetTcID)
		if err != nil {
			return err
		}
	}

	var toolWG sync.WaitGroup

	// Iterate over toolchains to get tools
	for _, tc := range tcInstances {
		// Get toolchain ids, double-checking if they are valid
		crnSplit := strings.Split(tc.ID, ":")
		if len(crnSplit) < 8 {
			fmt.Println("received invalid CRN format from Resource Controller, skipping...")
			continue
		}

		tcID := crnSplit[7]

		if !guidRegex.MatchString(tcID) {
			fmt.Println("received invalid CRN format from Resource Controller, skipping...")
			continue
		}

		if targetTcID != "" && tcID != targetTcID {
			continue
		}

		if tc.RegionID == region {
			tcName := normalizeResourceName(tc.Name, true)

			resourceMutex.Lock()
			g.Resources = append(g.Resources, g.loadToolchain(tcID, tcName))
			resourceMutex.Unlock()

			fmt.Println("=== FOUND TOOLCHAIN", tcID, "WITH NAME", tcName)

			// Get tools
			toolchainClientOptions := &cdtoolchainv2.CdToolchainV2Options{
				Authenticator: &core.IamAuthenticator{
					ApiKey: apiKey,
				},
			}

			toolchainClient, err := cdtoolchainv2.NewCdToolchainV2UsingExternalConfig(toolchainClientOptions)
			if err != nil {
				return err
			}

			listToolsOptions := toolchainClient.NewListToolsOptions(tcID)

			listToolsOptions.SetLimit(150) // 150 is max num tools per toolchain

			tools, _, err := toolchainClient.ListTools(listToolsOptions)
			if err != nil {
				return err
			}

			if includeS2S != "" {
				// Add toolchain's s2s policies (some tools require it)
				for _, pol := range s2sPolicies[tcID] {
					resourceMutex.Lock()
					g.Resources = append(g.Resources, g.loadAuthPolicies(*(pol.ID)))
					resourceMutex.Unlock()
				}
			}

			for _, t := range tools.Tools {
				toolType := *(t.ToolTypeID)
				tID := fmt.Sprintf("%s/%s", tcID, *(t.ID))

				// Name won't always exist in Parameters
				var tName string

				if t.Parameters["name"] != nil {
					tName = normalizeResourceName(t.Parameters["name"].(string), true)
				} else {
					tName = normalizeResourceName(toolType, true)
				}

				toolWG.Add(1)
				go g.HandleTool(t, toolType, tID, tName, &toolWG)
			}
		}
	}
	toolWG.Wait()
	return nil
}
