// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package aws

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The policies that keep a resource when its stack is deleted, or when an
// update replaces it: retainPolicy for both, and for DeletionPolicy also
// retainExceptOnCreate, which retains on every stack operation but the
// one that created the resource, so on deletion too.
const (
	retainPolicy         = "Retain"
	retainExceptOnCreate = "RetainExceptOnCreate"
)

// snapshotByDefault reports whether CloudFormation's default DeletionPolicy
// for a resource of cfnType, with the template's resource node, is
// Snapshot rather than Delete: an RDS DB cluster, and an RDS DB instance
// outside a cluster (no DBClusterIdentifier), as the DeletionPolicy
// attribute's reference says. Deleting the stack deletes those too, after
// a final snapshot.
func snapshotByDefault(cfnType string, resource *yaml.Node) bool {
	switch cfnType {
	case "AWS::RDS::DBCluster":
		return true
	case "AWS::RDS::DBInstance":
		return mappingValue(mappingValue(resource, "Properties"), "DBClusterIdentifier") == nil
	}
	return false
}

// snapshotTypes are the CloudFormation types whose DeletionPolicy may be
// Snapshot, as the DeletionPolicy attribute's reference lists them: a
// snapshot keeps their data, but the resource itself is deleted.
var snapshotTypes = map[string]bool{
	"AWS::DocDB::DBCluster":              true,
	"AWS::EC2::Volume":                   true,
	"AWS::ElastiCache::CacheCluster":     true,
	"AWS::ElastiCache::ReplicationGroup": true,
	"AWS::Neptune::DBCluster":            true,
	"AWS::RDS::DBCluster":                true,
	"AWS::RDS::DBInstance":               true,
	"AWS::Redshift::Cluster":             true,
}

// stackTemplate is a stack's template, as GetTemplate gives it, after its
// transforms (the Processed stage): its body, whether it is JSON, and the
// parsed document, whose node tree keeps the order, the comments and the
// short-form tags (!Ref) of a YAML template, so that a patched copy reads
// like the original.
type stackTemplate struct {
	body string
	json bool
	doc  *yaml.Node
}

// parseTemplate parses a template body, JSON or YAML, into one node tree.
// JSON is decoded as JSON, not as YAML, which rejects the tabs a JSON
// template may be indented with; it is written back as JSON.
func parseTemplate(body string) (*stackTemplate, error) {
	doc, err := parseTemplateNode(body)
	if err != nil {
		return nil, fmt.Errorf("parse the template: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("parse the template: not a mapping")
	}
	return &stackTemplate{body: body, json: isJSON(body), doc: doc}, nil
}

// isJSON reports whether a template body is JSON: a YAML template can't
// start with a brace and be a mapping of sections.
func isJSON(body string) bool {
	return strings.HasPrefix(strings.TrimSpace(body), "{")
}

// parseTemplateNode parses body into a document node.
func parseTemplateNode(body string) (*yaml.Node, error) {
	if isJSON(body) {
		decoder := json.NewDecoder(strings.NewReader(body))
		decoder.UseNumber()
		root, err := jsonNode(decoder)
		if err != nil {
			return nil, err
		}
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// jsonNode decodes the next JSON value of decoder into a node, keeping
// the order of an object's keys.
func jsonNode(decoder *json.Decoder) (*yaml.Node, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch v := token.(type) {
	case json.Delim:
		node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if v == '[' {
			node = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		}
		for decoder.More() {
			if node.Kind == yaml.MappingNode {
				key, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, _ := key.(string)
				node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name})
			}
			value, err := jsonNode(decoder)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return node, nil
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}, nil
	case json.Number:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: v.String()}, nil
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(v)}, nil
	default:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}
}

// resource returns the node of the template's resource logicalID, nil if
// the template has none.
func (t *stackTemplate) resource(logicalID string) *yaml.Node {
	if t == nil {
		return nil
	}
	return mappingValue(mappingValue(t.doc.Content[0], "Resources"), logicalID)
}

// retainProblems returns what keeps the resource logicalID, of cfnType,
// from outliving its stack: a DeletionPolicy other than Retain (or
// RetainExceptOnCreate), and an UpdateReplacePolicy other than Retain,
// each with the value it has, CloudFormation's default when it has none.
// None means both policies retain it.
func (t *stackTemplate) retainProblems(logicalID, cfnType string) []string {
	node := t.resource(logicalID)
	if node == nil {
		return []string{"not in the stack's template, so its policies can't be checked"}
	}
	var problems []string
	deletion := mappingValue(node, "DeletionPolicy")
	switch {
	case deletion == nil && snapshotByDefault(cfnType, node):
		problems = append(problems, "DeletionPolicy is Snapshot, the default for its type: deleting the stack deletes it after a final snapshot")
	case deletion == nil:
		problems = append(problems, "DeletionPolicy is Delete, the default")
	case deletion.Kind != yaml.ScalarNode:
		problems = append(problems, "DeletionPolicy is set by a function infraharvest doesn't evaluate")
	case deletion.Value == "Snapshot":
		problems = append(problems, "DeletionPolicy is Snapshot: deleting the stack deletes it after a final snapshot")
	case deletion.Value != retainPolicy && deletion.Value != retainExceptOnCreate:
		problems = append(problems, "DeletionPolicy is "+deletion.Value)
	}
	replace := mappingValue(node, "UpdateReplacePolicy")
	switch {
	case replace == nil:
		problems = append(problems, "UpdateReplacePolicy is Delete, the default")
	case replace.Kind != yaml.ScalarNode:
		problems = append(problems, "UpdateReplacePolicy is set by a function infraharvest doesn't evaluate")
	case replace.Value != retainPolicy:
		problems = append(problems, "UpdateReplacePolicy is "+replace.Value)
	}
	return problems
}

// withRetain returns the body of a copy of the template in which each of
// logicalIDs has DeletionPolicy and UpdateReplacePolicy Retain, but a
// DeletionPolicy of RetainExceptOnCreate, which retains on deletion
// already; the rest is as it was. A JSON template comes back as JSON,
// indented, a YAML one as YAML. The template itself is left as it is.
func (t *stackTemplate) withRetain(logicalIDs []string) (string, error) {
	doc, err := parseTemplateNode(t.body)
	if err != nil {
		return "", err
	}
	resources := mappingValue(doc.Content[0], "Resources")
	for _, id := range logicalIDs {
		node := mappingValue(resources, id)
		if node == nil || node.Kind != yaml.MappingNode {
			continue
		}
		if p := mappingValue(node, "DeletionPolicy"); p == nil || p.Kind != yaml.ScalarNode || p.Value != retainExceptOnCreate {
			setMappingValue(node, "DeletionPolicy", retainPolicy)
		}
		setMappingValue(node, "UpdateReplacePolicy", retainPolicy)
	}
	if t.json {
		var b bytes.Buffer
		if err := writeJSON(&b, doc.Content[0], ""); err != nil {
			return "", err
		}
		b.WriteByte('\n')
		return b.String(), nil
	}
	var b bytes.Buffer
	encoder := yaml.NewEncoder(&b)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

// refersTo reports whether the Properties of the template's resource
// logicalID name the resource other, or its physical ID: as a Ref or a
// Fn::GetAtt, in either form, or as a literal, such as the bucket a bucket
// policy is for.
func (t *stackTemplate) refersTo(logicalID, other, physicalID string) bool {
	properties := mappingValue(t.resource(logicalID), "Properties")
	found := false
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil || found {
			return
		}
		if n.Kind == yaml.ScalarNode {
			name, _, _ := strings.Cut(n.Value, ".")
			found = n.Value == other || n.Value == physicalID || (n.Tag == "!GetAtt" && name == other)
			return
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(properties)
	return found
}

// mappingValue returns the value of key in the mapping node, nil if node
// isn't a mapping or has no key.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// setMappingValue sets key of the mapping node to the string value: in
// place where node has it, else after its last key.
func setMappingValue(node *yaml.Node, key, value string) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
			return
		}
	}
	node.Content = append(node.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// writeJSON writes the node tree of a template parsed from JSON as JSON,
// indented by two spaces, keeping the order of its keys, which a map
// would lose.
func writeJSON(b *bytes.Buffer, n *yaml.Node, indent string) error {
	switch n.Kind {
	case yaml.MappingNode, yaml.SequenceNode:
		open, close, step := "{", "}", 2
		if n.Kind == yaml.SequenceNode {
			open, close, step = "[", "]", 1
		}
		if len(n.Content) == 0 {
			b.WriteString(open + close)
			return nil
		}
		b.WriteString(open)
		inner := indent + "  "
		for i := 0; i < len(n.Content); i += step {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString("\n" + inner)
			value := n.Content[i]
			if step == 2 {
				key, _ := json.Marshal(n.Content[i].Value)
				b.Write(key)
				b.WriteString(": ")
				value = n.Content[i+1]
			}
			if err := writeJSON(b, value, inner); err != nil {
				return err
			}
		}
		b.WriteString("\n" + indent + close)
		return nil
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!int", "!!float", "!!bool", "!!null":
			b.WriteString(n.Value)
		default:
			value, _ := json.Marshal(n.Value)
			b.Write(value)
		}
		return nil
	case yaml.AliasNode:
		return writeJSON(b, n.Alias, indent)
	}
	return fmt.Errorf("unexpected node kind %d in a JSON template", n.Kind)
}
