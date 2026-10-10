package api

import (
	"context"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	. "github.com/onsi/gomega"
)

// The spec must be valid and describe exactly the REST routes the server registers.
func TestOpenAPISpecCoversRoutes(t *testing.T) {
	g := NewWithT(t)
	doc, err := openapi3.NewLoader().LoadFromData(openapiSpec)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(doc.Validate(context.Background())).To(Succeed(), "invalid spec")
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
	g.Expect(served).To(ConsistOf(documented), "served routes must match the routes in openapi.yaml")
}
