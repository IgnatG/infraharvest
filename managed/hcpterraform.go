// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package managed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultHCPTerraformHost is the host of HCP Terraform (formerly Terraform
// Cloud); Terraform Enterprise has its own.
const DefaultHCPTerraformHost = "app.terraform.io"

// HCPTerraform reads the current state of HCP Terraform or Terraform
// Enterprise workspaces, through the API, as an ObjectStore: buckets are
// organizations, keys are workspace names.
type HCPTerraform struct {
	client *http.Client
	// base is the API's URL, https://<host>/api/v2.
	base  string
	token string
}

// NewHCPTerraform opens the API of host with the token Terraform uses for
// it: TF_TOKEN_<host> (dots as _, hyphens as __), or the one terraform
// login saved in credentials.tfrc.json.
func NewHCPTerraform(_ context.Context, host string) (ObjectStore, error) {
	token, err := hcpTerraformToken(host, os.Getenv)
	if err != nil {
		return nil, err
	}
	return &HCPTerraform{client: &http.Client{Timeout: time.Minute}, base: "https://" + host + "/api/v2", token: token}, nil
}

// hcpTerraformToken returns the API token for host, as Terraform finds it.
func hcpTerraformToken(host string, getenv func(string) string) (string, error) {
	name := "TF_TOKEN_" + strings.NewReplacer(".", "_", "-", "__").Replace(host)
	if token := getenv(name); token != "" {
		return token, nil
	}
	path, err := credentialsFile(getenv)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err == nil {
		var file struct {
			Credentials map[string]struct {
				Token string `json:"token"`
			} `json:"credentials"`
		}
		if err := json.Unmarshal(content, &file); err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		if token := file.Credentials[host].Token; token != "" {
			return token, nil
		}
	}
	return "", fmt.Errorf("no API token for %s: run terraform login %s, or set %s", host, host, name)
}

// credentialsFile is where terraform login saves tokens.
func credentialsFile(getenv func(string) string) (string, error) {
	if runtime.GOOS == "windows" {
		if appData := getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "terraform.d", "credentials.tfrc.json"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".terraform.d", "credentials.tfrc.json"), nil
}

// List returns the names of the organization's workspaces that start with
// prefix.
func (h *HCPTerraform) List(ctx context.Context, organization, prefix string) ([]string, error) {
	var names []string
	for page := 1; page != 0; {
		query := url.Values{"page[number]": {strconv.Itoa(page)}, "page[size]": {"100"}}
		if prefix != "" {
			query.Set("search[name]", prefix)
		}
		var resp struct {
			Data []struct {
				Attributes struct {
					Name string `json:"name"`
				} `json:"attributes"`
			} `json:"data"`
			Meta struct {
				Pagination struct {
					NextPage int `json:"next-page"`
				} `json:"pagination"`
			} `json:"meta"`
		}
		if err := h.get(ctx, h.base+"/organizations/"+url.PathEscape(organization)+"/workspaces?"+query.Encode(), &resp); err != nil {
			return nil, err
		}
		for _, w := range resp.Data {
			// The search matches anywhere in the name.
			if strings.HasPrefix(w.Attributes.Name, prefix) {
				names = append(names, w.Attributes.Name)
			}
		}
		page = resp.Meta.Pagination.NextPage
	}
	return names, nil
}

// Get returns the current state of the organization's workspace, nil if it
// has none yet.
func (h *HCPTerraform) Get(ctx context.Context, organization, workspace string) ([]byte, error) {
	var w struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := h.get(ctx, h.base+"/organizations/"+url.PathEscape(organization)+"/workspaces/"+url.PathEscape(workspace), &w); err != nil {
		return nil, err
	}
	var version struct {
		Data struct {
			Attributes struct {
				DownloadURL string `json:"hosted-state-download-url"`
			} `json:"attributes"`
		} `json:"data"`
	}
	err := h.get(ctx, h.base+"/workspaces/"+url.PathEscape(w.Data.ID)+"/current-state-version", &version)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if version.Data.Attributes.DownloadURL == "" {
		return nil, nil
	}
	return h.download(ctx, version.Data.Attributes.DownloadURL)
}

var errNotFound = errors.New("not found")

// get decodes the JSON:API document at u into v.
func (h *HCPTerraform) get(ctx context.Context, u string, v any) error {
	body, err := h.fetch(ctx, u, "application/vnd.api+json")
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// download returns the state at u.
func (h *HCPTerraform) download(ctx context.Context, u string) ([]byte, error) {
	return h.fetch(ctx, u, "application/json")
}

// fetch returns the content at u. Only the API's host gets the token: state
// downloads are signed URLs, which can be on another host.
func (h *HCPTerraform) fetch(ctx context.Context, u, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, err
	}
	if api, err := url.Parse(h.base); err == nil && req.URL.Host == api.Host {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	req.Header.Set("Accept", accept)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%s: %w", u, errNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512<<20))
}
