package api

import (
	_ "embed"
	"net/http"
)

// openapi.yaml describes the REST routes in restRoutes; openapi_test.go keeps the two in sync and
// validates the API tests' traffic against it.
//
//go:embed openapi.yaml
var openapiSpec []byte

// docsPage loads Scalar (pinned, integrity-checked) in the browser and points it at the spec. Its
// "Try it" requests go to this server with the token entered under Authorize. Scalar's hosted
// extras (AI chat, developer toolbar, MCP generator) are off: they send the spec to Scalar's cloud.
const docsPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Terrarium API</title>
</head>
<body>
<script id="api-reference" data-url="/openapi.yaml"
  data-configuration='{"theme":"deepSpace","darkMode":true,"showDeveloperTools":"never","agent":{"disabled":true},"mcp":{"disabled":true}}'></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.73.1/dist/browser/standalone.js"
  integrity="sha384-kYDGzV91Jnn3TbHINV3nt54riK2uMJDfN5Al8dAkz4FssELTBWbD8rgw32sTKfOi"
  crossorigin="anonymous"></script>
</body>
</html>
`

func serveSpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(openapiSpec)
}

func serveDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(docsPage))
}
