// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !minimal || aws

package cli

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/IgnatG/infraharvest/internal/importer"
	"github.com/IgnatG/infraharvest/internal/mcpserver"
	"github.com/IgnatG/infraharvest/internal/selection"
)

// cfnSeedsStack is the stack the fake AWS of cfnAWS has: a custom resource
// alone, which no service lists, so that a command scoped to it lists
// nothing but the stack.
const cfnSeedsStack = "arn:aws:cloudformation:eu-west-2:123456789012:stack/seeds/1"

// cfnRequest is a request the fake AWS got: its action, the stack it
// names, if any, and its Authorization header, which names the access key
// and the region it was signed for.
type cfnRequest struct {
	action, stack, authorization string
}

// cfnFake is the fake AWS the commands of these tests reach, through
// AWS_ENDPOINT_URL: STS and CloudFormation, over the query protocol. One
// serves the whole test binary, as the provider keeps the configurations
// it builds, endpoint and all, for the process.
var cfnFake struct {
	once     sync.Once
	server   *httptest.Server
	mu       sync.Mutex
	requests []cfnRequest
}

// cfnAWS points the AWS SDK at the fake, with keys of its own, and the
// profile's region, eu-west-2, from a shared configuration file, and
// returns a func that returns the requests the fake has had since.
func cfnAWS(t *testing.T) func() []cfnRequest {
	t.Helper()
	cfnFake.once.Do(func() {
		cfnFake.server = httptest.NewServer(http.HandlerFunc(cfnHandle))
	})
	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	credentials := filepath.Join(dir, "credentials")
	if err := os.WriteFile(config, []byte("[default]\nregion = eu-west-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentials, []byte("[default]\naws_access_key_id = AKIDFAKE\naws_secret_access_key = secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"AWS_ENDPOINT_URL":            cfnFake.server.URL,
		"AWS_CONFIG_FILE":             config,
		"AWS_SHARED_CREDENTIALS_FILE": credentials,
		"AWS_EC2_METADATA_DISABLED":   "true",
		"AWS_REGION":                  "",
		"AWS_DEFAULT_REGION":          "",
		"AWS_PROFILE":                 "",
		"AWS_ACCESS_KEY_ID":           "",
		"AWS_SECRET_ACCESS_KEY":       "",
		"AWS_SESSION_TOKEN":           "",
		"AWS_MAX_ATTEMPTS":            "1",
	} {
		t.Setenv(key, value)
	}
	cfnFake.mu.Lock()
	start := len(cfnFake.requests)
	cfnFake.mu.Unlock()
	return func() []cfnRequest {
		cfnFake.mu.Lock()
		defer cfnFake.mu.Unlock()
		return append([]cfnRequest(nil), cfnFake.requests[start:]...)
	}
}

// cfnHandle answers STS and CloudFormation.
func cfnHandle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	form, _ := url.ParseQuery(string(body))
	action, stack := form.Get("Action"), form.Get("StackName")
	cfnFake.mu.Lock()
	cfnFake.requests = append(cfnFake.requests, cfnRequest{action: action, stack: stack, authorization: r.Header.Get("Authorization")})
	cfnFake.mu.Unlock()
	const stsNS, cfnNS = "https://sts.amazonaws.com/doc/2011-06-15/", "http://cloudformation.amazonaws.com/doc/2010-05-15/"
	answer := func(ns, result string) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, `<`+action+`Response xmlns="`+ns+`"><`+action+`Result>`+result+`</`+action+`Result></`+action+`Response>`)
	}
	switch action {
	case "AssumeRole":
		answer(stsNS, `<Credentials><AccessKeyId>ASIAFAKE</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>token</SessionToken><Expiration>2099-01-01T00:00:00Z</Expiration></Credentials>`+
			`<AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/importer/infraharvest</Arn><AssumedRoleId>AROAFAKE:infraharvest</AssumedRoleId></AssumedRoleUser>`)
	case "GetCallerIdentity":
		answer(stsNS, `<Account>123456789012</Account><Arn>arn:aws:iam::123456789012:user/importer</Arn><UserId>AIDAFAKE</UserId>`)
	case "DescribeStacks":
		if stack != "seeds" && stack != cfnSeedsStack {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `<ErrorResponse xmlns="`+cfnNS+`"><Error><Type>Sender</Type><Code>ValidationError</Code><Message>Stack with id `+stack+` does not exist</Message></Error></ErrorResponse>`)
			return
		}
		answer(cfnNS, `<Stacks><member><StackId>`+cfnSeedsStack+`</StackId><StackName>seeds</StackName><StackStatus>CREATE_COMPLETE</StackStatus><CreationTime>2026-01-01T00:00:00Z</CreationTime></member></Stacks>`)
	case "GetTemplate":
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte("Resources:\n  Seed:\n    Type: Custom::Seed\n    Properties:\n      ServiceToken: arn:aws:lambda:eu-west-2:123456789012:function:seed\n"))
		answer(cfnNS, `<TemplateBody>`+b.String()+`</TemplateBody>`)
	case "ListStackResources":
		answer(cfnNS, `<StackResourceSummaries><member><LogicalResourceId>Seed</LogicalResourceId><PhysicalResourceId>seed-1</PhysicalResourceId>`+
			`<ResourceType>Custom::Seed</ResourceType><ResourceStatus>CREATE_COMPLETE</ResourceStatus><LastUpdatedTimestamp>2026-01-01T00:00:00Z</LastUpdatedTimestamp></member></StackResourceSummaries>`)
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `<ErrorResponse><Error><Type>Sender</Type><Code>UnexpectedCall</Code><Message>`+action+`</Message></Error></ErrorResponse>`)
	}
}

// signedBy reports whether a request was signed with the access key key,
// for region and service.
func signedBy(r cfnRequest, key, region, service string) bool {
	return strings.Contains(r.authorization, "Credential="+key+"/") && strings.Contains(r.authorization, "/"+region+"/"+service+"/aws4_request")
}

// runRoot runs infraharvest with args in this process, as the binary
// would, and returns its exit code.
func runRoot(ctx context.Context, args ...string) int {
	root := NewCmdRoot()
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs(args)
	return importer.ExitCode(root.ExecuteContext(ctx))
}

// discover aws --cfn-stack, without --regions or --services: the stack's
// name is looked for in the profile's region, and as nothing of the stack
// is listed, discover lists nothing and writes the selection file.
func TestDiscoverCloudFormationStack(t *testing.T) {
	requests := cfnAWS(t)
	path := filepath.Join(t.TempDir(), "selection.yaml")

	if code := runRoot(t.Context(), "discover", "aws", "--cfn-stack=seeds", "--selection="+path, "--pick=false", "--out="+t.TempDir()); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	if _, err := selection.Load(path); err != nil {
		t.Errorf("no selection file: %v", err)
	}
	var described bool
	for _, r := range requests() {
		if r.action == "DescribeStacks" && r.stack == "seeds" {
			described = true
			if !signedBy(r, "AKIDFAKE", "eu-west-2", "cloudformation") {
				t.Errorf("DescribeStacks not in the profile's region: %s", r.authorization)
			}
		}
	}
	if !described {
		t.Errorf("the stack wasn't looked for: %+v", requests())
	}
}

// With an --assume-role ARN, the stacks are read as that role.
func TestCloudFormationStackAssumesRole(t *testing.T) {
	requests := cfnAWS(t)
	const role = "arn:aws:iam::123456789012:role/importer"

	if code := runRoot(t.Context(), "discover", "aws", "--cfn-stack=seeds", "--assume-role="+role, "--selection="+filepath.Join(t.TempDir(), "selection.yaml"), "--pick=false", "--out="+t.TempDir()); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	assumed, read := false, false
	for _, r := range requests() {
		switch r.action {
		case "AssumeRole":
			assumed = true
		case "ListStackResources", "GetTemplate":
			read = true
			if !signedBy(r, "ASIAFAKE", "eu-west-2", "cloudformation") {
				t.Errorf("%s not as the role: %s", r.action, r.authorization)
			}
		}
	}
	if !assumed || !read {
		t.Errorf("assumed %v, read %v: %+v", assumed, read, requests())
	}
}

// A configuration file that sets cfn-stack, and no services, runs the
// command, as --cfn-stack on the command line does.
func TestCloudFormationStackFromConfig(t *testing.T) {
	requests := cfnAWS(t)
	config := writeConfig(t, "version: 1\nproviders:\n  aws:\n    cfn-stack: [seeds]\n")

	if code := runRoot(t.Context(), "discover", "aws", "--config="+config, "--selection="+filepath.Join(t.TempDir(), "selection.yaml"), "--pick=false", "--out="+t.TempDir()); code != 0 {
		t.Fatalf("exit code %d", code)
	}

	for _, r := range requests() {
		if r.action == "ListStackResources" && r.stack == cfnSeedsStack {
			return
		}
	}
	t.Errorf("the configured stack wasn't read: %+v", requests())
}

// The MCP server's discover tool, with a scope of cfn_stacks and no
// services, runs discover aws --cfn-stack, here through cobra in this
// process.
func TestMCPDiscoverCloudFormationStack(t *testing.T) {
	requests := cfnAWS(t)
	root := t.TempDir()
	run := func(ctx context.Context, args []string) ([]byte, []byte, int, error) {
		return nil, nil, runRoot(ctx, args...), nil
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := mcpserver.New("test", root, run, checkRun).Connect(t.Context(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "discover", Arguments: map[string]any{
		"provider":   "aws",
		"cfn_stacks": []string{"seeds"},
		"selection":  filepath.Join(root, "selection.yaml"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("discover failed: %+v", res.Content)
	}
	for _, r := range requests() {
		if r.action == "DescribeStacks" && r.stack == "seeds" {
			return
		}
	}
	t.Errorf("the scope's stack wasn't looked for: %+v", requests())
}
