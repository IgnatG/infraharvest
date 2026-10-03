package aws

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/IgnatG/infraharvest/terraformutils"
	"github.com/aws/aws-sdk-go-v2/aws"
)

// apiCall is an AWS API request as seen by a fake handler.
type apiCall struct {
	Op    string     // operation, for JSON and query protocols (e.g. ListQueues)
	Path  string     // URL path, for REST protocols (e.g. /thing-types)
	Query url.Values // URL query, for REST protocols
	Body  string
}

// fakeAPI is an aws.HTTPClient that answers every request with handle.
type fakeAPI struct {
	handle func(apiCall) string
}

func (f *fakeAPI) Do(r *http.Request) (*http.Response, error) {
	body := ""
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		body = string(b)
	}
	call := apiCall{Path: r.URL.Path, Query: r.URL.Query(), Body: body}
	contentType := "application/json"
	if target := r.Header.Get("X-Amz-Target"); target != "" {
		call.Op = target[strings.LastIndex(target, ".")+1:]
		contentType = r.Header.Get("Content-Type")
	} else if form, err := url.ParseQuery(body); err == nil && form.Get("Action") != "" {
		call.Op = form.Get("Action")
		contentType = "text/xml"
	}
	respBody := f.handle(call)
	status := http.StatusOK
	switch {
	case strings.Contains(respBody, `"__type"`):
		// AWS JSON protocols name the error in __type and send a 4xx status.
		status = http.StatusBadRequest
	case strings.HasPrefix(respBody, "<Error>"):
		// REST-XML (S3) errors; S3's "not configured" errors are 404s.
		status = http.StatusNotFound
		contentType = "application/xml"
	case strings.HasPrefix(respBody, "<") && contentType == "application/json":
		contentType = "application/xml" // REST-XML (S3)
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(respBody)),
		Request:    r,
	}, nil
}

// useFakeAPI makes the AWS generators in this package send their requests to
// handle instead of AWS.
func useFakeAPI(t *testing.T, handle func(apiCall) string) {
	t.Helper()
	cfg := aws.Config{
		Region:      "us-east-1",
		Credentials: aws.AnonymousCredentials{},
		HTTPClient:  &fakeAPI{handle: handle},
		Retryer:     func() aws.Retryer { return aws.NopRetryer{} },
	}
	previous := testConfig
	testConfig = &cfg
	t.Cleanup(func() { testConfig = previous })
}

// resourceIDs returns the IDs of the resources of resourceType.
func resourceIDs(resources []terraformutils.Resource, resourceType string) []string {
	var ids []string
	for _, r := range resources {
		if r.InstanceInfo.Type == resourceType {
			ids = append(ids, r.InstanceState.ID)
		}
	}
	return ids
}
