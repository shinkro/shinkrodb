// Package testutil provides offline HTTP fixtures for importer regression tests.
package testutil

import (
	"io"
	"net/http"
	"strings"
)

type RoundTripFunc func(*http.Request) (*http.Response, error)

func (f RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func Response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)),
		Header: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}}}
}
