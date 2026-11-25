// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package tfstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/IgnatG/infraharvest/internal/tftypes"
)

// Document is what one state holds that tools which read infrastructure
// need. It holds no attribute values and no output values, which state
// keeps in the open and which are most often secrets.
type Document struct {
	// Source is where the state was found: the path of a file, or the
	// location of an object, such as s3://bucket/key or tfc://org/workspace.
	Source string
	// Serial counts the changes made to the state; 0 where the state has
	// none.
	Serial int64
	// Lineage identifies the state across its serials; "" where the state
	// has none.
	Lineage string
	// TerraformVersion is the version of Terraform or OpenTofu that wrote
	// the state; "" where the state has none.
	TerraformVersion string
	// Instances are the resource instances of the state, managed and data
	// sources, in the order the state has them.
	Instances []Instance
	// Outputs are the root module's outputs: their names and whether they
	// are sensitive, never their values.
	Outputs []Output
	// RemoteStates are the places the state's terraform_remote_state data
	// sources read other states from.
	RemoteStates []RemoteState
}

// Instance is one instance of a resource of a state: one resource when it
// has no count or for_each, or one of the many it has.
type Instance struct {
	// Mode is "managed" for a resource and "data" for a data source.
	Mode string
	// Module is the module the resource is in, such as module.vpc or
	// module.app["a"].module.db; "" for the root module.
	Module string
	// Type and Name are the resource's, such as aws_s3_bucket and logs.
	Type, Name string
	// Address is where Terraform addresses the instance: module.vpc.aws_subnet.a[0],
	// data.aws_caller_identity.current. It is "" for a resource the state
	// doesn't name.
	Address string
	// Provider is the provider's address as the state writes it, such as
	// provider["registry.terraform.io/hashicorp/aws"], with its module path
	// if it is in a module; "" where the state has none.
	Provider string
	// ID is the instance's id attribute, or its arn attribute where it has
	// no id; ARN is its arn attribute, "" where it has none. Both are
	// "" for a resource that has neither.
	ID, ARN string
	// IdentityID is the ID of the instance's resource identity, which the
	// state of a resource has from Terraform 1.12 on, in the form
	// Terraform writes an identity in its own log, such as
	// account_id=123456789012,name=logs,region=eu-west-2; "" where it has
	// none.
	IdentityID string
	// Dependencies are the addresses of the resources the instance
	// depends on, without their instance keys, as the state records them.
	Dependencies []string
}

// Output is an output of the root module: its name, and whether
// Terraform treats its value as sensitive. Its value is never kept.
type Output struct {
	Name      string
	Sensitive bool
}

// RemoteState is a terraform_remote_state data source of a state: where
// it reads another state from. Only the fields that say where are kept,
// never a credential, whatever the data source's configuration holds. A
// field is "" where the backend has no such setting or the data source
// doesn't set it.
type RemoteState struct {
	// Address is the data source's address in this state.
	Address string
	// Backend is the backend's name: s3, gcs, azurerm, remote, local.
	Backend string
	// Account is the Azure storage account of an azurerm backend.
	Account string
	// Bucket is the bucket of an s3 or gcs backend, or the container of an
	// azurerm one.
	Bucket string
	// Key is the key of the state's object in an s3 or azurerm backend,
	// the prefix of a gcs one, or the path of a local one.
	Key string
	// Host is the hostname of a remote backend; "" for HCP Terraform.
	Host string
	// Organization is the organization of a remote backend.
	Organization string
	// Workspace is the workspace the data source reads: the name of a
	// remote backend's workspace, or the workspace setting of another
	// backend's data source.
	Workspace string
}

// Parse is ParseFrom of the state content, found at where.
func Parse(content []byte, where string) (Document, error) {
	return ParseFrom(bytes.NewReader(content), where)
}

// ParseFrom reads the state in, found at where, in format version 4, as
// Terraform 0.12 and later and OpenTofu write it. It decodes the state as
// it reads: one resource of it is held at a time, and of it only what
// Document keeps, so that a large state doesn't take its size in memory
// twice over. It fails on a state of more than 512 MiB (see ReadState),
// and on version 3, from Terraform 0.11 and earlier, with a hint to
// upgrade it. A failure returns no Document.
func ParseFrom(in io.Reader, where string) (Document, error) {
	doc := Document{Source: where}
	version, err := decodeState(json.NewDecoder(&limitedState{r: in}), &doc)
	if err != nil {
		var tooLarge *stateTooLarge
		if errors.As(err, &tooLarge) {
			return Document{}, tooLarge
		}
		return Document{}, fmt.Errorf("%s: %w", where, err)
	}
	switch version {
	case 4:
	case 3:
		return Document{}, errors.New(where + ": state format version 3 is not supported; upgrade it with Terraform 0.12 or later")
	default:
		return Document{}, errors.New(where + ": not a version 4 state file")
	}
	return doc, nil
}

// lenientString, lenientStrings, lenientInt and lenientBool read a value of
// a state that infraharvest has always left alone, such as a resource's
// provider: one of the wrong type is left empty rather than failing the
// whole state, as it would never have.
type lenientString string

func (s *lenientString) UnmarshalJSON(b []byte) error {
	var v string
	if json.Unmarshal(b, &v) == nil {
		*s = lenientString(v)
	}
	return nil
}

type lenientStrings []string

func (s *lenientStrings) UnmarshalJSON(b []byte) error {
	var items []any
	if json.Unmarshal(b, &items) == nil {
		for _, item := range items {
			if v, ok := item.(string); ok {
				*s = append(*s, v)
			}
		}
	}
	return nil
}

type lenientInt int64

func (n *lenientInt) UnmarshalJSON(b []byte) error {
	var v float64
	if json.Unmarshal(b, &v) == nil {
		*n = lenientInt(v)
	}
	return nil
}

type lenientBool bool

func (n *lenientBool) UnmarshalJSON(b []byte) error {
	var v bool
	if json.Unmarshal(b, &v) == nil {
		*n = lenientBool(v)
	}
	return nil
}

// stateResource is what ParseFrom reads of a resource of a state.
type stateResource struct {
	Mode      string          `json:"mode"`
	Module    string          `json:"module"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Provider  lenientString   `json:"provider"`
	Instances []stateInstance `json:"instances"`
}

// stateInstance is what ParseFrom reads of an instance of a resource.
type stateInstance struct {
	IndexKey     any            `json:"index_key"`
	Identity     map[string]any `json:"identity"`
	Dependencies lenientStrings `json:"dependencies"`
	Attributes   struct {
		ID  string `json:"id"`
		ARN string `json:"arn"`
		// Backend, Config and Workspace are a terraform_remote_state's
		// own, and kept raw: another resource's attribute of the same name
		// may be of any type. Its outputs, which hold values, are not
		// read at all.
		Backend   json.RawMessage `json:"backend"`
		Config    json.RawMessage `json:"config"`
		Workspace json.RawMessage `json:"workspace"`
	} `json:"attributes"`
}

// decodeState reads a state from dec as encoding/json would unmarshal it
// into a struct of its version and its resources (see stateResource),
// one resource at a time, adding what a Document keeps of each to doc as
// it goes, and returns its version. Keys match as encoding/json matches
// them, whatever their case, and of a key given twice, the last counts;
// for resources, encoding/json would decode the second array into the
// elements of the first, which no state has.
func decodeState(dec *json.Decoder, doc *Document) (int, error) {
	tok, err := dec.Token()
	if err != nil {
		return 0, err
	}
	version := 0
	switch tok {
	case nil:
		// null, which unmarshals into the zero state.
	case json.Delim('{'):
		for dec.More() {
			tok, err := dec.Token()
			if err != nil {
				return 0, err
			}
			name, _ := tok.(string)
			if err := decodeField(dec, doc, name, &version); err != nil {
				return 0, err
			}
		}
		if _, err := dec.Token(); err != nil { // the closing brace
			return 0, err
		}
	default:
		return 0, fmt.Errorf("a state is a JSON object, not %v", tok)
	}
	// Nothing may follow the state, as encoding/json.Unmarshal says.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("invalid character after top-level value")
		}
		return 0, err
	}
	return version, nil
}

// decodeField reads the value of the field name of a state's top level
// from dec, into doc or version, or skips it.
func decodeField(dec *json.Decoder, doc *Document, name string, version *int) error {
	switch {
	case strings.EqualFold(name, "version"):
		return dec.Decode(version)
	case strings.EqualFold(name, "resources"):
		doc.Instances, doc.RemoteStates = nil, nil
		return decodeResources(dec, doc)
	case strings.EqualFold(name, "serial"):
		var serial lenientInt
		err := dec.Decode(&serial)
		doc.Serial = int64(serial)
		return err
	case strings.EqualFold(name, "lineage"):
		var lineage lenientString
		err := dec.Decode(&lineage)
		doc.Lineage = string(lineage)
		return err
	case strings.EqualFold(name, "terraform_version"):
		var v lenientString
		err := dec.Decode(&v)
		doc.TerraformVersion = string(v)
		return err
	case strings.EqualFold(name, "outputs"):
		doc.Outputs = nil
		return decodeOutputs(dec, doc)
	default:
		return skipValue(dec)
	}
}

// decodeOutputs reads the outputs object of a state from dec, keeping each
// output's name and whether it is sensitive, and none of its value.
func decodeOutputs(dec *json.Decoder, doc *Document) error {
	return eachField(dec, func(name string) error {
		out := Output{Name: name}
		err := eachField(dec, func(field string) error {
			if strings.EqualFold(field, "sensitive") {
				var sensitive lenientBool
				err := dec.Decode(&sensitive)
				out.Sensitive = bool(sensitive)
				return err
			}
			return skipValue(dec)
		})
		doc.Outputs = append(doc.Outputs, out)
		return err
	})
}

// eachField reads the value at dec, and, if it is an object, calls field
// with the name of each of its fields, which reads the field's value. Any
// other value is skipped, whole.
func eachField(dec *json.Decoder, field func(name string) error) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
	case json.Delim('['):
		return skipRest(dec)
	default:
		return nil
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		name, _ := tok.(string)
		if err := field(name); err != nil {
			return err
		}
	}
	_, err = dec.Token() // the closing brace
	return err
}

// decodeResources reads the resources array of a state from dec, one
// resource at a time, adding what is kept of each to doc.
func decodeResources(dec *json.Decoder, doc *Document) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case nil:
		return nil
	case json.Delim('['):
	default:
		return fmt.Errorf("a state's resources are an array, not %v", tok)
	}
	for dec.More() {
		var res stateResource
		if err := dec.Decode(&res); err != nil {
			return err
		}
		addResource(doc, res)
	}
	_, err = dec.Token() // the closing bracket
	return err
}

// addResource adds the instances of res, one resource of a state, to doc,
// and, for a terraform_remote_state data source, where it reads from.
func addResource(doc *Document, res stateResource) {
	for _, i := range res.Instances {
		inst := Instance{
			Mode: res.Mode, Module: res.Module, Type: res.Type, Name: res.Name,
			Address:  address(res.Mode, res.Module, res.Type, res.Name, i.IndexKey),
			Provider: string(res.Provider),
			ID:       i.Attributes.ID, ARN: i.Attributes.ARN,
			Dependencies: i.Dependencies,
		}
		if inst.ID == "" {
			inst.ID = inst.ARN
		}
		// What Terraform's list resources return is listed by its
		// identity's ID, which the state of a resource has from
		// Terraform 1.12 on, whatever it was imported by.
		if identity, ok := tftypes.IdentityValues(i.Identity); ok {
			inst.IdentityID = tftypes.IdentityID(identity)
		}
		doc.Instances = append(doc.Instances, inst)
		if res.Mode == "data" && res.Type == "terraform_remote_state" {
			doc.RemoteStates = append(doc.RemoteStates, remoteState(inst.Address, i))
		}
	}
}

// remoteState returns where the terraform_remote_state data source at
// address, whose instance in the state is i, reads its state from. It
// reads the backend's name and the settings that name the state, and
// nothing else of the configuration.
func remoteState(address string, i stateInstance) RemoteState {
	r := RemoteState{Address: address}
	var backend lenientString
	_ = json.Unmarshal(i.Attributes.Backend, &backend)
	r.Backend = string(backend)
	var workspace lenientString
	_ = json.Unmarshal(i.Attributes.Workspace, &workspace)
	r.Workspace = string(workspace)
	if r.Workspace == "default" {
		r.Workspace = ""
	}

	// The configuration is of a dynamic type, which state writes as
	// {"value": {...}, "type": [...]}; a plain object is read too.
	var config map[string]json.RawMessage
	if json.Unmarshal(i.Attributes.Config, &config) != nil {
		return r
	}
	if value, ok := config["value"]; ok {
		if _, hasType := config["type"]; hasType {
			config = nil
			_ = json.Unmarshal(value, &config)
		}
	}
	str := func(name string) string {
		var v lenientString
		_ = json.Unmarshal(config[name], &v)
		return string(v)
	}
	switch r.Backend {
	case "s3":
		r.Bucket, r.Key = str("bucket"), str("key")
	case "gcs":
		r.Bucket, r.Key = str("bucket"), str("prefix")
	case "azurerm":
		r.Account, r.Bucket, r.Key = str("storage_account_name"), str("container_name"), str("key")
	case "local":
		r.Key = str("path")
	case "remote":
		r.Host, r.Organization = str("hostname"), str("organization")
		var workspaces map[string]json.RawMessage
		if json.Unmarshal(config["workspaces"], &workspaces) == nil {
			var name lenientString
			_ = json.Unmarshal(workspaces["name"], &name)
			if name != "" {
				r.Workspace = string(name)
			}
		}
	}
	return r
}

// skipValue reads the next value from dec, whatever it is, without
// keeping it.
func skipValue(dec *json.Decoder) error {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth == 0 {
			return nil
		}
	}
}

// skipRest reads from dec to the end of the object or array whose opening
// it has read.
func skipRest(dec *json.Decoder) error {
	for depth := 1; depth > 0; {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
	return nil
}

// limitedState reads r, and fails once it has read more than
// stateSizeLimit bytes (see ReadState).
type limitedState struct {
	r    io.Reader
	read int64
}

// stateTooLarge is the error of a state larger than stateSizeLimit.
type stateTooLarge struct{}

func (*stateTooLarge) Error() string {
	return fmt.Sprintf("state is larger than %d MiB, the most infraharvest reads", stateSizeLimit>>20)
}

func (l *limitedState) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.read += int64(n)
	if l.read > stateSizeLimit {
		return n, &stateTooLarge{}
	}
	return n, err
}

// address returns the address state gives the instance with index, its
// instance key (a number for count, a string for for_each, or none), of
// the resource of typ named name in module, such as
// module.vpc.aws_subnet.a[0], or, for a data source (mode data),
// module.vpc.data.aws_subnet.a[0]; "" if the state names no resource.
func address(mode, module, typ, name string, index any) string {
	if name == "" {
		return ""
	}
	a := typ + "." + name
	if mode == "data" {
		a = "data." + a
	}
	if module != "" {
		a = module + "." + a
	}
	switch k := index.(type) {
	case float64:
		a += "[" + strconv.FormatFloat(k, 'f', -1, 64) + "]"
	case string:
		a += "[" + strconv.Quote(k) + "]"
	}
	return a
}
