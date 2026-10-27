// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"

	"google.golang.org/api/googleapi"
)

// retryAttempts is how many times a request is sent at most: the first
// time and up to four retries.
const retryAttempts = 5

// retryMaxDelay caps the wait before a retry, and the Retry-After an
// answer asks for: one that asks for longer is returned as it is.
const retryMaxDelay = 30 * time.Second

// retryDelay is the wait before the first retry, doubled before each
// next one, with jitter (see retryWait). Tests make it shorter.
var retryDelay = 500 * time.Millisecond

// retryTransport sends each request through base again when Google's API
// answers that it is throttled (429, or a 403 whose reason says so, see
// rateLimited) or failed for the time being (500, 502, 503, 504), or the
// connection fails: the generated REST clients
// send a request once, and a quota's 429 on any page would fail the
// service's listing. Every call the listers make reads, the POST of
// getIamPolicy among them, so each may be sent again. It waits between
// attempts (see retryWait), and stops when the request's context ends.
// base is the rate limiter's transport, so that each retry is paced too.
type retryTransport struct {
	base http.RoundTripper
	// delay replaces retryDelay when set.
	delay time.Duration
}

func (t retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// A body that can't be read again can't be sent again.
	replayable := req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
	for attempt := 1; ; attempt++ {
		send := req
		if attempt > 1 && req.Body != nil && req.Body != http.NoBody {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			send = req.Clone(req.Context())
			send.Body = body
		}
		resp, err := t.base.RoundTrip(send)
		if !replayable || attempt == retryAttempts || req.Context().Err() != nil || !retryable(resp, err) {
			return resp, err
		}
		delay := t.delay
		if delay == 0 {
			delay = retryDelay
		}
		wait, ok := retryWait(attempt, resp, delay)
		if !ok {
			return resp, err
		}
		if resp != nil {
			// Read the answer to the end, so the connection can be
			// used again.
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			_ = resp.Body.Close()
		}
		timer := time.NewTimer(wait)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}
}

// retryable reports whether an attempt's answer, or its error, is one
// that may pass: a status of 429 or 500, 502, 503 or 504, a 403 that says
// the caller is throttled (see rateLimited), or a network error, such as
// a connection reset.
func retryable(resp *http.Response, err error) bool {
	if err != nil {
		var netErr net.Error
		return errors.As(err, &netErr) || errors.Is(err, io.ErrUnexpectedEOF)
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case http.StatusForbidden:
		return rateLimited(resp)
	default:
		return false
	}
}

// rateLimitReasons are the reasons of a 403 that says the caller is
// throttled, rather than refused: Compute Engine, Cloud DNS, the SQL Admin
// API and Cloud Resource Manager v1 throttle with a 403, not a 429, whose
// error items give rateLimitExceeded or userRateLimitExceeded, or whose
// google.rpc.ErrorInfo detail gives RATE_LIMIT_EXCEEDED. A quota spent for
// the day, quotaExceeded, doesn't pass with a retry.
var rateLimitReasons = map[string]bool{
	"rateLimitExceeded":     true,
	"userRateLimitExceeded": true,
	"RATE_LIMIT_EXCEEDED":   true,
}

// rateLimitBodyLimit caps how much of a 403's body rateLimited reads to
// find its reason: Google's error bodies are far shorter.
const rateLimitBodyLimit = 1 << 16

// rateLimited reports whether a 403 answer says the caller is throttled
// (see rateLimitReasons), so that a permission denied still fails at
// once. It reads the answer's body to find the reason, and puts back what
// it read, so that the client still reads the whole error when the answer
// isn't retried.
func rateLimited(resp *http.Response) bool {
	body, err := io.ReadAll(io.LimitReader(resp.Body, rateLimitBodyLimit))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
	if err != nil {
		return false
	}
	var apiErr *googleapi.Error
	if !errors.As(googleapi.CheckResponseWithBody(resp, body), &apiErr) {
		return false
	}
	for _, item := range apiErr.Errors {
		if rateLimitReasons[item.Reason] {
			return true
		}
	}
	for _, detail := range apiErr.Details {
		if info, ok := detail.(map[string]interface{}); ok {
			if reason, _ := info["reason"].(string); rateLimitReasons[reason] {
				return true
			}
		}
	}
	return false
}

// retryWait is how long to wait after attempt (1 for the first) before
// the next: delay doubled for each attempt before, up to
// retryMaxDelay, half of it fixed and half random, so that parallel
// listings don't retry in step; or the answer's Retry-After, in seconds
// or as a date, when it asks for longer. It is false when Retry-After asks
// for longer than retryMaxDelay.
func retryWait(attempt int, resp *http.Response, delay time.Duration) (time.Duration, bool) {
	wait := min(delay<<(attempt-1), retryMaxDelay)
	if half := wait / 2; half > 0 {
		wait = half + rand.N(half)
	}
	if resp == nil {
		return wait, true
	}
	after, ok := retryAfter(resp.Header.Get("Retry-After"))
	if !ok {
		return wait, true
	}
	if after > retryMaxDelay {
		return 0, false
	}
	return max(wait, after), true
}

// retryAfter parses a Retry-After header: a number of seconds, or an HTTP
// date. It is false when there is none, or it can't be read.
func retryAfter(header string) (time.Duration, bool) {
	if header == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if at, err := http.ParseTime(header); err == nil {
		return max(time.Until(at), 0), true
	}
	return 0, false
}
