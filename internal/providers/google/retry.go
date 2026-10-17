// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

package google

import (
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"
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
// answers that it is throttled (429) or failed for the time being (500,
// 502, 503, 504), or the connection fails: the generated REST clients
// send a request once, and a quota's 429 on any page would fail the
// service's listing. Every call the listers make reads, the POST of
// getIamPolicy among them, so each may be sent again. It waits between
// attempts (see retryWait), and stops when the request's context ends.
// base is the rate limiter's transport, so that each retry is paced too.
type retryTransport struct {
	base http.RoundTripper
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
		wait, ok := retryWait(attempt, resp)
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
// that may pass: a status of 429 or 500, 502, 503 or 504, or a network
// error, such as a connection reset.
func retryable(resp *http.Response, err error) bool {
	if err != nil {
		var netErr net.Error
		return errors.As(err, &netErr) || errors.Is(err, io.ErrUnexpectedEOF)
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// retryWait is how long to wait after attempt (1 for the first) before
// the next: retryDelay doubled for each attempt before, up to
// retryMaxDelay, half of it fixed and half random, so that parallel
// listings don't retry in step; or the answer's Retry-After, in seconds
// or as a date, when it asks for longer. It is false when Retry-After asks
// for longer than retryMaxDelay.
func retryWait(attempt int, resp *http.Response) (time.Duration, bool) {
	wait := min(retryDelay<<(attempt-1), retryMaxDelay)
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
