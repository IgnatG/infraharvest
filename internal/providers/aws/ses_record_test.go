// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"strings"
	"testing"
)

const recordSESNS = "http://ses.amazonaws.com/doc/2010-12-01/"

// recordSESAPI answers the SES lister, failing the operation failing. Each
// list has two pages.
func recordSESAPI(t *testing.T, failing string) func(apiCall) string {
	return func(call apiCall) string {
		if call.Op == failing {
			return recordQueryError(recordSESNS, "AccessDenied")
		}
		second := recordSecondPage(call)
		var result string
		switch call.Op {
		case "ListIdentities":
			switch {
			case strings.Contains(call.Body, "IdentityType=Domain") && second:
				result = `<Identities><member>mail.example.com</member></Identities>`
			case strings.Contains(call.Body, "IdentityType=Domain"):
				result = `<Identities><member>example.com</member></Identities><NextToken>` + nextPageToken + `</NextToken>`
			case strings.Contains(call.Body, "IdentityType=EmailAddress") && second:
				result = `<Identities><member>ops@example.com</member></Identities>`
			case strings.Contains(call.Body, "IdentityType=EmailAddress"):
				result = `<Identities><member>alerts@example.com</member></Identities><NextToken>` + nextPageToken + `</NextToken>`
			default:
				return recordUnexpected(t, call)
			}
		case "ListTemplates":
			if second {
				result = `<TemplatesMetadata><member><Name>second-page</Name></member></TemplatesMetadata>`
			} else {
				result = `<TemplatesMetadata>
					<member><Name>welcome</Name><CreatedTimestamp>2026-01-02T03:04:05Z</CreatedTimestamp></member>
					<member><Name>reset</Name><CreatedTimestamp>2026-01-02T03:04:05Z</CreatedTimestamp></member>
					</TemplatesMetadata><NextToken>` + nextPageToken + `</NextToken>`
			}
		case "ListConfigurationSets":
			if second {
				result = `<ConfigurationSets><member><Name>second-page</Name></member></ConfigurationSets>`
			} else {
				result = `<ConfigurationSets><member><Name>tracking</Name></member></ConfigurationSets><NextToken>` + nextPageToken + `</NextToken>`
			}
		case "ListReceiptRuleSets":
			if second {
				result = `<RuleSets><member><Name>second-page</Name></member></RuleSets>`
			} else {
				result = `<RuleSets>
					<member><Name>inbound</Name><CreatedTimestamp>2026-01-02T03:04:05Z</CreatedTimestamp></member>
					<member><Name>spare</Name><CreatedTimestamp>2026-01-02T03:04:05Z</CreatedTimestamp></member>
					</RuleSets><NextToken>` + nextPageToken + `</NextToken>`
			}
		case "DescribeReceiptRuleSet":
			switch {
			case strings.Contains(call.Body, "RuleSetName=inbound"):
				result = `<Metadata><Name>inbound</Name></Metadata><Rules>
					<member><Name>store</Name><Enabled>true</Enabled><ScanEnabled>true</ScanEnabled><TlsPolicy>Optional</TlsPolicy></member>
					<member><Name>bounce</Name><Enabled>false</Enabled></member>
					</Rules>`
			case strings.Contains(call.Body, "RuleSetName=spare"):
				result = `<Metadata><Name>spare</Name></Metadata><Rules/>`
			case strings.Contains(call.Body, "RuleSetName=second-page"):
				result = `<Metadata><Name>second-page</Name></Metadata><Rules><member><Name>forward</Name><Enabled>true</Enabled></member></Rules>`
			default:
				return recordUnexpected(t, call)
			}
		default:
			return recordUnexpected(t, call)
		}
		return recordQuery(call.Op, recordSESNS, result)
	}
}

// The lister lists, each across pages, domain identities, email identities,
// templates, configuration sets and receipt rule sets, each set followed
// by its rules, by <rule set>:<rule>.
func TestRecordSesListsIdentitiesTemplatesAndRules(t *testing.T) {
	useFakeAPI(t, recordSESAPI(t, ""))
	g := &SesLister{}

	if err := g.List(); err != nil {
		t.Fatal(err)
	}

	assertRecorded(t, g.Resources,
		recordedResource{"aws_ses_domain_identity", "example.com", "example.com", nil},
		recordedResource{"aws_ses_domain_identity", "mail.example.com", "mail.example.com", nil},
		recordedResource{"aws_ses_email_identity", "alerts@example.com", "alerts@example.com", nil},
		recordedResource{"aws_ses_email_identity", "ops@example.com", "ops@example.com", nil},
		recordedResource{"aws_ses_template", "welcome", "welcome", nil},
		recordedResource{"aws_ses_template", "reset", "reset", nil},
		recordedResource{"aws_ses_template", "second-page", "second-page", nil},
		recordedResource{"aws_ses_configuration_set", "tracking", "tracking", nil},
		recordedResource{"aws_ses_configuration_set", "second-page", "second-page", nil},
		recordedResource{"aws_ses_receipt_rule_set", "inbound", "inbound", nil},
		recordedResource{"aws_ses_receipt_rule", "inbound:store", "inbound:store", map[string]string{"name": "store", "rule_set_name": "inbound"}},
		recordedResource{"aws_ses_receipt_rule", "inbound:bounce", "inbound:bounce", map[string]string{"name": "bounce", "rule_set_name": "inbound"}},
		recordedResource{"aws_ses_receipt_rule_set", "spare", "spare", nil},
		recordedResource{"aws_ses_receipt_rule_set", "second-page", "second-page", nil},
		recordedResource{"aws_ses_receipt_rule", "second-page:forward", "second-page:forward", map[string]string{"name": "forward", "rule_set_name": "second-page"}},
	)
}

func TestRecordSesReturnsErrors(t *testing.T) {
	for _, failing := range []string{"ListIdentities", "ListTemplates", "ListConfigurationSets", "ListReceiptRuleSets", "DescribeReceiptRuleSet"} {
		t.Run(failing, func(t *testing.T) {
			useFakeAPI(t, recordSESAPI(t, failing))
			g := &SesLister{}

			assertRecordedError(t, g.List(), "AccessDenied")
		})
	}
}
