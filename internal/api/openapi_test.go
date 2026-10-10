package api

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// The spec must be valid and describe exactly the REST routes the server registers.
func TestOpenAPISpecCoversRoutes(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData(openapiSpec)
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("invalid spec: %v", err)
	}
	var documented []string
	for path, item := range doc.Paths.Map() {
		for method := range item.Operations() {
			documented = append(documented, strings.ToUpper(method)+" "+path)
		}
	}
	var served []string
	for _, rt := range (&Server{}).restRoutes() {
		served = append(served, rt.pattern)
	}
	slices.Sort(documented)
	slices.Sort(served)
	for _, r := range served {
		if !slices.Contains(documented, r) {
			t.Errorf("route %s is not in openapi.yaml", r)
		}
	}
	for _, r := range documented {
		if !slices.Contains(served, r) {
			t.Errorf("openapi.yaml documents %s, which is not served", r)
		}
	}
}
