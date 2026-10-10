package api_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

var (
	specOnce   sync.Once
	specRouter routers.Router
	specErr    error
)

func loadSpecRouter() (routers.Router, error) {
	specOnce.Do(func() {
		var doc *openapi3.T
		if doc, specErr = openapi3.NewLoader().LoadFromFile("openapi.yaml"); specErr != nil {
			return
		}
		// Match any host: the spec's relative server URL resolves against the test server.
		doc.Servers = nil
		specRouter, specErr = gorillamux.NewRouter(doc)
	})
	return specRouter, specErr
}

// specClient checks the API tests' traffic against openapi.yaml: every response, and every request
// the server accepted (rejected requests are often deliberately invalid).
func specClient(t *testing.T) *http.Client {
	t.Helper()
	router, err := loadSpecRouter()
	if err != nil {
		t.Fatalf("load openapi.yaml: %v", err)
	}
	return &http.Client{Transport: specTransport{t, router}}
}

type specTransport struct {
	t      *testing.T
	router routers.Router
}

func (st specTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		return resp, err
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	check := req.Clone(context.Background())
	check.Body = io.NopCloser(bytes.NewReader(reqBody))
	route, params, err := st.router.FindRoute(check)
	if err != nil {
		st.t.Errorf("%s %s: not in openapi.yaml: %v", req.Method, req.URL.Path, err)
		return resp, nil
	}
	in := &openapi3filter.RequestValidationInput{
		Request: check, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
	}
	if resp.StatusCode < 300 {
		if err := openapi3filter.ValidateRequest(context.Background(), in); err != nil {
			st.t.Errorf("%s %s: accepted request does not match openapi.yaml: %v", req.Method, req.URL, err)
		}
	}
	out := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: in, Status: resp.StatusCode, Header: resp.Header,
		Body: io.NopCloser(bytes.NewReader(respBody)),
	}
	if err := openapi3filter.ValidateResponse(context.Background(), out); err != nil {
		st.t.Errorf("%s %s -> %d: response does not match openapi.yaml: %v", req.Method, req.URL, resp.StatusCode, err)
	}
	return resp, nil
}
