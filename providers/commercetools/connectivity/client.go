package connectivity

import (
	"strings"

	"github.com/labd/commercetools-go-sdk/platform"
	"golang.org/x/oauth2/clientcredentials"
)

const userAgent = "infraharvest"

// NewClient returns a client for the configured project, authenticated with
// the OAuth2 client credentials flow.
func (c *Config) NewClient() (*platform.ByProjectKeyRequestBuilder, error) {
	client, err := platform.NewClient(&platform.ClientConfig{
		URL: c.BaseURL,
		Credentials: &clientcredentials.Config{
			ClientID:     c.ClientID,
			ClientSecret: c.ClientSecret,
			Scopes:       strings.Split(c.ClientScope, " "),
			TokenURL:     c.TokenURL,
		},
		UserAgent: userAgent + " " + platform.GetUserAgent(),
	})
	if err != nil {
		return nil, err
	}
	return client.WithProjectKey(c.ProjectKey), nil
}
