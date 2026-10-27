// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package hclname

import (
	"fmt"
	"strings"
)

// An AWS::SSM::Parameter::Value<...> parameter's value is the name of an
// SSM parameter, whose value CloudFormation reads from Parameter Store
// when it deploys. Its variable holds the name, as the template's
// parameter does, and an aws_ssm_parameter data source reads the value, so
// that the value, which may be one only the parameter's readers should
// see, never becomes a literal in the configuration.

const ssmValuePrefix = "AWS::SSM::Parameter::Value<"

// SSMParameterType reports whether a parameter type is
// AWS::SSM::Parameter::Value<...>, and whether the value it reads is a
// list (List<...> or CommaDelimitedList), which Parameter Store holds
// comma-separated.
func SSMParameterType(t string) (ssm, list bool) {
	if !strings.HasPrefix(t, ssmValuePrefix) || !strings.HasSuffix(t, ">") {
		return false, false
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(t, ssmValuePrefix), ">")
	return true, inner == "CommaDelimitedList" || strings.HasPrefix(inner, "List<")
}

// SSMDescription returns what the variable of an SSM parameter type,
// cfnType, says it holds.
func SSMDescription(cfnType string) string {
	return fmt.Sprintf("The name of the SSM parameter to read the value of: the CloudFormation type was %s.", cfnType)
}

// SSMData returns the aws_ssm_parameter data source, labelled label, that
// reads the SSM parameter var.<variable> names.
func SSMData(label, variable string) string {
	return fmt.Sprintf("data \"aws_ssm_parameter\" %q {\n  name = var.%s\n}\n", label, variable)
}

// SSMValue returns the expression that reads the value of the data source
// SSMData labels label: its insecure_value, which suits the String and
// StringList parameters CloudFormation can read (it can't read a
// SecureString as a parameter type), split at commas for a list.
func SSMValue(label string, list bool) string {
	e := "data.aws_ssm_parameter." + label + ".insecure_value"
	if list {
		return `split(",", ` + e + ")"
	}
	return e
}
