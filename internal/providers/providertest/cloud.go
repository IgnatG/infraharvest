// Copyright 2026 Gabriel Ignat
// SPDX-License-Identifier: AGPL-3.0-only

// Package providertest stands up fake clouds for the providers' tests, at
// the HTTP level, so that a test doesn't depend on how a lister builds its
// clients: a Cloud answers every request its Handler is asked, whether the
// SDK sends it through the Cloud as its HTTP client (AWS, Azure) or to the
// Cloud's server (Google). Contract runs every lister of a provider
// against one, as each must behave alike. It links no cloud SDK: each
// provider's tests shape the answers of their own cloud.
package providertest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// Request is a request as the fake cloud's handler sees it.
type Request struct {
	Method string
	Host   string
	Path   string
	Query  url.Values
	Header http.Header
	Body   string
}

// Answer is what the fake cloud answers a request with: its status, 200
// for 0; its content type, application/json for ""; any other headers;
// and its body.
type Answer struct {
	Status      int
	ContentType string
	Header      http.Header
	Body        string
}

// JSON is a 200 answer with a JSON body.
func JSON(body string) Answer {
	return Answer{Status: http.StatusOK, Body: body}
}

// Status is an answer of status with a JSON body, such as an error.
func Status(status int, body string) Answer {
	return Answer{Status: status, Body: body}
}

// Handler answers each request the fake cloud gets. Requests may come at
// once.
type Handler func(Request) Answer

// Cloud is a fake cloud: it answers every request with its handler, and
// records the requests in the order they came. It is an HTTP client (as
// aws.HTTPClient and the Azure SDK's policy.Transporter take one, with
// Do), a round tripper, and an http.Handler (see Serve). A request whose
// context is done gets its context's error, as from a real transport,
// and isn't handled.
type Cloud struct {
	handle Handler

	mu       sync.Mutex
	requests []Request
}

// New returns a cloud that answers with handle.
func New(handle Handler) *Cloud {
	return &Cloud{handle: handle}
}

// Do answers r.
func (c *Cloud) Do(r *http.Request) (*http.Response, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	answer, err := c.answer(r)
	if err != nil {
		return nil, err
	}
	header := answer.header()
	return &http.Response{
		Status:     http.StatusText(answer.status()),
		StatusCode: answer.status(),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(answer.Body)),
		Request:    r,
	}, nil
}

// RoundTrip answers r, as Do does.
func (c *Cloud) RoundTrip(r *http.Request) (*http.Response, error) {
	return c.Do(r)
}

// ServeHTTP answers r on w.
func (c *Cloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	answer, err := c.answer(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	for k, v := range answer.header() {
		w.Header()[k] = v
	}
	w.WriteHeader(answer.status())
	_, _ = io.WriteString(w, answer.Body)
}

// Serve starts a server that the cloud answers, closed when t ends, and
// returns its URL, for a client given an endpoint.
func (c *Cloud) Serve(t testing.TB) string {
	t.Helper()
	server := httptest.NewServer(c)
	t.Cleanup(server.Close)
	return server.URL
}

// Requests returns the requests the cloud got, in order.
func (c *Cloud) Requests() []Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Request(nil), c.requests...)
}

// Paths returns the paths of the requests the cloud got, in order.
func (c *Cloud) Paths() []string {
	var paths []string
	for _, r := range c.Requests() {
		paths = append(paths, r.Path)
	}
	return paths
}

// Bodies returns the bodies of the requests the cloud got, in order.
func (c *Cloud) Bodies() []string {
	var bodies []string
	for _, r := range c.Requests() {
		bodies = append(bodies, r.Body)
	}
	return bodies
}

// answer records r and returns the handler's answer to it.
func (c *Cloud) answer(r *http.Request) (Answer, error) {
	body := ""
	if r.Body != nil {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return Answer{}, err
		}
		body = string(b)
	}
	req := Request{Method: r.Method, Host: r.Host, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: body}
	if req.Host == "" {
		req.Host = r.URL.Host
	}
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	return c.handle(req), nil
}

func (a Answer) status() int {
	if a.Status == 0 {
		return http.StatusOK
	}
	return a.Status
}

func (a Answer) header() http.Header {
	header := a.Header.Clone()
	if header == nil {
		header = http.Header{}
	}
	contentType := a.ContentType
	if contentType == "" {
		contentType = "application/json"
	}
	header.Set("Content-Type", contentType)
	return header
}

// Pages answers the requests it gets with bodies, one each in order, and
// any after the last with a 500.
func Pages(bodies ...string) Handler {
	var mu sync.Mutex
	next := 0
	return func(Request) Answer {
		mu.Lock()
		defer mu.Unlock()
		if next == len(bodies) {
			return Status(http.StatusInternalServerError, `{"error":{"message":"no page left"}}`)
		}
		next++
		return JSON(bodies[next-1])
	}
}
