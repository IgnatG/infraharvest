// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package okta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/okta/okta-sdk-golang/v5/okta"
)

// rawClient makes GET requests to Okta endpoints that okta-sdk-golang/v5 has
// no typed call for (the org factor API) or cannot decode (rules of an
// MFA_ENROLL policy). It uses the SDK client's org URL, API token and HTTP
// client.
type rawClient struct {
	orgURL     *url.URL
	token      string
	userAgent  string
	httpClient *http.Client
	// retryBackoff is the wait before retrying a rate-limited request whose
	// response names no reset time; zero means defaultRetryBackoff.
	retryBackoff time.Duration
}

const (
	// rateLimitAttempts caps the attempts at a rate-limited request.
	rateLimitAttempts = 4
	// defaultRetryBackoff is the wait before retrying a rate-limited request
	// when the response does not say when the limit resets.
	defaultRetryBackoff = time.Second
	// maxRateLimitWait caps the wait for a rate limit to reset.
	maxRateLimitWait = 2 * time.Minute
)

func newRawClient(client *okta.APIClient) (*rawClient, error) {
	config := client.GetConfig()
	orgURL, err := url.Parse(config.Okta.Client.OrgUrl)
	if err != nil {
		return nil, err
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &rawClient{
		orgURL:     orgURL,
		token:      config.Okta.Client.Token,
		userAgent:  config.UserAgent,
		httpClient: httpClient,
	}, nil
}

// resolve returns the org URL with the path and query of ref, so requests
// (and the token they carry) only ever go to the configured org.
func (c *rawClient) resolve(ref string) (string, error) {
	u, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	target := *c.orgURL
	target.Path = u.Path
	target.RawPath = u.RawPath
	target.RawQuery = u.RawQuery
	target.Fragment = ""
	return target.String(), nil
}

// rawList GETs path and decodes the JSON array it returns, following the
// rel="next" Link headers Okta paginates with.
func rawList[T any](ctx context.Context, c *rawClient, path string) ([]T, error) {
	var all []T
	next := path
	for next != "" {
		target, err := c.resolve(next)
		if err != nil {
			return nil, err
		}
		var page []T
		header, err := c.get(ctx, target, &page)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		next = nextLink(header)
	}
	return all, nil
}

// get GETs target and decodes its JSON body into out. A rate-limited (429)
// request is retried, up to rateLimitAttempts attempts in all, once the
// limit resets.
func (c *rawClient) get(ctx context.Context, target string, out interface{}) (http.Header, error) {
	for attempt := 1; ; attempt++ {
		header, wait, err := c.getOnce(ctx, target, out)
		if wait == 0 || attempt == rateLimitAttempts {
			return header, err
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// getOnce makes one request. When the request was rate limited, it returns
// the error with how long to wait before trying again.
func (c *rawClient) getOnce(ctx context.Context, target string, out interface{}) (http.Header, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "SSWS "+c.token)
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		err := fmt.Errorf("okta: GET %s: %s: %s", req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, c.rateLimitWait(resp.Header, time.Now()), err
		}
		return nil, 0, err
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return nil, 0, fmt.Errorf("okta: GET %s: decoding response: %w", req.URL.Path, err)
	}
	return resp.Header, 0, nil
}

// rateLimitWait returns how long to wait after a 429 response: until the
// X-Rate-Limit-Reset epoch time it names, or the client's backoff when the
// header is missing, invalid or already past. It never waits longer than
// maxRateLimitWait.
func (c *rawClient) rateLimitWait(header http.Header, now time.Time) time.Duration {
	wait := c.retryBackoff
	if wait <= 0 {
		wait = defaultRetryBackoff
	}
	if reset, err := strconv.ParseInt(header.Get("X-Rate-Limit-Reset"), 10, 64); err == nil {
		if untilReset := time.Unix(reset, 0).Sub(now); untilReset > wait {
			wait = untilReset
		}
	}
	if wait > maxRateLimitWait {
		wait = maxRateLimitWait
	}
	return wait
}

// nextLink returns the target of the rel="next" Link header, or "".
func nextLink(header http.Header) string {
	for _, value := range header.Values("Link") {
		for _, link := range strings.Split(value, ",") {
			parts := strings.Split(link, ";")
			target := strings.TrimSpace(parts[0])
			if len(parts) < 2 || !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
				continue
			}
			for _, param := range parts[1:] {
				param = strings.ReplaceAll(strings.TrimSpace(param), " ", "")
				if param == `rel="next"` || param == "rel=next" {
					return strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">")
				}
			}
		}
	}
	return ""
}
