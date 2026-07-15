package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPublicV1OpenAPIRoutesMatchRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1/public")
	RegisterPublicChannelContextRoute(v1)
	RegisterPublicChannelRoutes(v1)

	actual := []string{}
	for _, route := range r.Routes() {
		actual = append(actual, route.Method+" "+normalizeContractPath(strings.TrimPrefix(route.Path, "/api/v1/public")))
	}
	sort.Strings(actual)

	_, file, _, _ := runtime.Caller(0)
	contractPath := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "docs", "openapi", "public-v1.json"))
	body, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		OpenAPI string                            `json:"openapi"`
		Paths   map[string]map[string]interface{} `json:"paths"`
	}
	if err := json.Unmarshal(body, &contract); err != nil {
		t.Fatal(err)
	}
	if contract.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %q", contract.OpenAPI)
	}
	expected := []string{}
	httpMethods := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true, "head": true, "options": true, "trace": true}
	for path, methods := range contract.Paths {
		for method, rawOperation := range methods {
			if !httpMethods[strings.ToLower(method)] {
				continue
			}
			if operation, ok := rawOperation.(map[string]interface{}); ok {
				if service, _ := operation["x-service"].(string); service != "" && service != "marketing" {
					continue
				}
			}
			expected = append(expected, strings.ToUpper(method)+" "+path)
		}
	}
	sort.Strings(expected)
	if strings.Join(actual, "\n") != strings.Join(expected, "\n") {
		t.Fatalf("public v1 route drift\nruntime:\n%s\ncontract:\n%s", strings.Join(actual, "\n"), strings.Join(expected, "\n"))
	}
}

func normalizeContractPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") {
			parts[i] = "{" + strings.TrimPrefix(part, ":") + "}"
		}
	}
	return strings.Join(parts, "/")
}
