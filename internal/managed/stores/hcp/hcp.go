// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package hcp reads the current state of HCP Terraform (formerly Terraform
// Cloud) or Terraform Enterprise workspaces through their API. It is plain
// HTTP and links no cloud SDK, so every build includes it.
package hcp

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
	"sync"
	"time"

	svchost "github.com/hashicorp/terraform-svchost"

	"github.com/IgnatG/infraharvest/internal/managed"
)

// Store reads the current state of HCP Terraform or Terraform Enterprise
// workspaces, through the API, as an ObjectStore: buckets are
// organizations, keys are workspace names.
type Store struct {
	client *http.Client
	// base is the API's URL, https://<host>/api/v2.
	base  string
	token string
	// ids are the IDs of the workspaces List found, by organization and
	// name, so that Get needn't look them up again.
	ids sync.Map
	// retryBase is the wait before the first retry of a request the API
	// throttled or failed (see fetch); 0 is defaultRetryBase.
	retryBase time.Duration
}

const (
	// fetchAttempts is how many times fetch asks for a URL the API
	// throttles (429) or fails (5xx).
	fetchAttempts = 4
	// defaultRetryBase is the wait before the first retry, doubled before
	// each next one, unless the answer's Retry-After says otherwise.
	defaultRetryBase = time.Second
	// maxRetryWait caps the wait a Retry-After asks for.
	maxRetryWait = 30 * time.Second
	// headerTimeout is how long the API, or the host a state downloads
	// from, has to start answering a request. The body has fetchTimeout:
	// a state can be large, and a total timeout as short as headerTimeout
	// would cut its download.
	headerTimeout = time.Minute
	// fetchTimeout is the longest one request, its body included, may
	// take.
	fetchTimeout = 15 * time.Minute
)

// New opens the API of host with the token Terraform uses for it:
// TF_TOKEN_<host> (dots as _, hyphens as __), or the one terraform login
// saved in credentials.tfrc.json. host must be a hostname, with an
// optional port, as Terraform takes it; it is normalised as Terraform
// does (lower case, Punycode, no :443), so that it gets the token
// Terraform would send to it and no other.
func New(_ context.Context, host string) (managed.ObjectStore, error) {
	hostname, err := svchost.ForComparison(host)
	if err != nil {
		return nil, fmt.Errorf("HCP Terraform host %q: %w", host, err)
	}
	token, err := apiToken(hostname, os.Environ(), os.Getenv)
	if err != nil {
		return nil, err
	}
	base := "https://" + string(hostname) + "/api/v2"
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = headerTimeout
	return &Store{client: &http.Client{Transport: transport, CheckRedirect: tokenOnlyTo(base)}, base: base, token: token}, nil
}

// apiToken returns the API token for host, as Terraform finds it: from the
// last TF_TOKEN_ variable of environ whose name decodes to host, or else
// from the credentials file entry whose key normalises to host.
func apiToken(host svchost.Hostname, environ []string, getenv func(string) string) (string, error) {
	var token string
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if h, ok := tokenHost(name); ok && h == host {
			token = value
		}
	}
	if token != "" {
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
		for key, c := range file.Credentials {
			if h, err := svchost.ForComparison(key); err == nil && h == host && c.Token != "" {
				return c.Token, nil
			}
		}
	}
	name := "TF_TOKEN_" + strings.NewReplacer(".", "_", "-", "__").Replace(string(host))
	return "", fmt.Errorf("no API token for %s: run terraform login %s, or set %s", host, host, name)
}

// tokenHost returns the host a TF_TOKEN_ variable's name holds the token
// of, decoded as Terraform does: __ as a hyphen, then _ as a dot, then
// normalised. ok is false for another variable, or one that names no
// valid host.
func tokenHost(name string) (host svchost.Hostname, ok bool) {
	raw, ok := strings.CutPrefix(name, "TF_TOKEN_")
	if !ok {
		return "", false
	}
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "__", "-"), "_", ".")
	host, err := svchost.ForComparison(svchost.ForDisplay(raw))
	return host, err == nil
}

// tokenOnlyTo returns a redirect policy that keeps the token only on
// redirects to base's host over https: Go would also send it to that
// host over http, and to its subdomains.
func tokenOnlyTo(base string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if !sendsToken(base, req.URL) {
			req.Header.Del("Authorization")
		}
		return nil
	}
}

// sendsToken reports whether a request to u may carry the token: only
// one over https to the API's host.
func sendsToken(base string, u *url.URL) bool {
	api, err := url.Parse(base)
	return err == nil && u.Scheme == "https" && u.Host == api.Host
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
func (h *Store) List(ctx context.Context, organization, prefix string) ([]string, error) {
	var names []string
	for page := 1; page != 0; {
		query := url.Values{"page[number]": {strconv.Itoa(page)}, "page[size]": {"100"}}
		if prefix != "" {
			query.Set("search[name]", prefix)
		}
		var resp struct {
			Data []struct {
				ID         string `json:"id"`
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
				if w.ID != "" {
					h.ids.Store(organization+"/"+w.Attributes.Name, w.ID)
				}
			}
		}
		page = resp.Meta.Pagination.NextPage
	}
	return names, nil
}

// Get returns the current state of the organization's workspace, nil if it
// has none yet.
func (h *Store) Get(ctx context.Context, organization, workspace string) ([]byte, error) {
	u, what, err := h.stateURL(ctx, organization, workspace)
	if err != nil || u == "" {
		return nil, err
	}
	return h.fetch(ctx, u, what, "application/json")
}

// Open returns the current state of the organization's workspace to read
// as it goes (see managed.StreamStore), nil if it has none yet.
func (h *Store) Open(ctx context.Context, organization, workspace string) (io.ReadCloser, error) {
	u, what, err := h.stateURL(ctx, organization, workspace)
	if err != nil || u == "" {
		return nil, err
	}
	rc, err := h.open(ctx, u, what, "application/json")
	if err != nil {
		return nil, err
	}
	return rc, nil
}

// Stores read state as it is decoded.
var _ managed.StreamStore = (*Store)(nil)

// stateURL returns the signed URL the workspace's current state downloads
// from, and what errors call that download, or "" if it has no state.
func (h *Store) stateURL(ctx context.Context, organization, workspace string) (u, what string, err error) {
	id, err := h.workspaceID(ctx, organization, workspace)
	if err != nil {
		return "", "", err
	}
	var version struct {
		Data struct {
			Attributes struct {
				DownloadURL string `json:"hosted-state-download-url"`
			} `json:"attributes"`
		} `json:"data"`
	}
	err = h.get(ctx, h.base+"/workspaces/"+url.PathEscape(id)+"/current-state-version", &version)
	if errors.Is(err, errNotFound) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return version.Data.Attributes.DownloadURL, "downloading the state of workspace " + organization + "/" + workspace, nil
}

// workspaceID returns the ID of the organization's workspace: the one List
// found, or else the API's.
func (h *Store) workspaceID(ctx context.Context, organization, workspace string) (string, error) {
	if id, ok := h.ids.Load(organization + "/" + workspace); ok {
		return id.(string), nil
	}
	var w struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := h.get(ctx, h.base+"/organizations/"+url.PathEscape(organization)+"/workspaces/"+url.PathEscape(workspace), &w); err != nil {
		return "", err
	}
	return w.Data.ID, nil
}

var errNotFound = errors.New("not found")

// get decodes the JSON:API document at u into v.
func (h *Store) get(ctx context.Context, u string, v any) error {
	body, err := h.fetch(ctx, u, u, "application/vnd.api+json")
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// fetch returns the content at u, which errors name as what. Only the API's
// host gets the token, and over https: state downloads are signed URLs,
// which can be on another host, and which errors, logs and an MCP client
// must never see, so no error quotes u unless what is u. A request the
// API throttles (429) or fails (5xx) is asked again, up to fetchAttempts
// times in all, after the wait its Retry-After asks for, or else one that
// doubles each time.
func (h *Store) fetch(ctx context.Context, u, what, accept string) ([]byte, error) {
	body, err := h.open(ctx, u, what, accept)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	content, err := managed.ReadState(body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, managed.WithoutURL(err))
	}
	return content, nil
}

// open asks for u as fetch does, with its retries, and returns the body to
// read as it goes, which the caller closes. The request's time limit runs
// until then. Reading it is the caller's to bound (see managed.ParseFrom).
func (h *Store) open(ctx context.Context, u, what, accept string) (io.ReadCloser, error) {
	wait := h.retryBase
	if wait == 0 {
		wait = defaultRetryBase
	}
	for attempt := 1; ; attempt++ {
		body, retryAfter, err := h.openOnce(ctx, u, what, accept)
		if retryAfter < 0 || attempt == fetchAttempts {
			return body, err
		}
		if retryAfter > 0 {
			wait = min(retryAfter, maxRetryWait)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("%s: %w", what, ctx.Err())
		case <-timer.C:
		}
		wait *= 2
	}
}

// openOnce asks for u once, as open does. A retryAfter of 0 or more
// says the answer may be different later: the wait the answer's
// Retry-After asks for, or 0 when it asks for none; it is -1 otherwise.
func (h *Store) openOnce(ctx context.Context, u, what, accept string) (body io.ReadCloser, retryAfter time.Duration, err error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer func() {
		if err != nil {
			cancel()
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return nil, -1, fmt.Errorf("%s: %w", what, managed.WithoutURL(err))
	}
	if sendsToken(h.base, req.URL) {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	req.Header.Set("Accept", accept)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, -1, fmt.Errorf("%s: %w", what, managed.WithoutURL(err))
	}
	defer func() {
		if err != nil {
			resp.Body.Close()
		}
	}()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, -1, fmt.Errorf("%s: %w", what, errNotFound)
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()), fmt.Errorf("%s: %s", what, resp.Status)
	case resp.StatusCode != http.StatusOK:
		return nil, -1, fmt.Errorf("%s: %s", what, resp.Status)
	}
	return &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}, -1, nil
}

// cancelOnClose ends the request a body belongs to when it is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	defer c.cancel()
	return c.ReadCloser.Close()
}

// parseRetryAfter returns the wait a Retry-After header value asks for, in
// seconds or as an HTTP date after now, or 0 for none or one it can't
// read.
func parseRetryAfter(value string, now time.Time) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return max(time.Duration(seconds)*time.Second, 0)
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(at.Sub(now), 0)
	}
	return 0
}
