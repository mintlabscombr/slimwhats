package handlers

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed swagger_ui.html
var swaggerHTML []byte

// Embed the *rendered* OpenAPI spec (openapi.gen.yaml), not the source
// (openapi.yaml). The renderer substitutes APP_URL in at build time so
// the served spec points at the right address. See the Makefile target
// `render-openapi` (and the matching step in the Dockerfile) — both
// regenerate this file before `go build` / `go test` so the embed
// pattern always has a file to pick up.
//
//go:embed openapi.gen.yaml
var specFS embed.FS

// SwaggerUIHandler serves the embedded Swagger UI HTML page.
func SwaggerUIHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", swaggerHTML)
	}
}

// OpenAPISpecHandler serves the embedded OpenAPI 3 spec.
func OpenAPISpecHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		data, err := specFS.ReadFile("openapi.gen.yaml")
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "spec_missing",
				"message": err.Error(),
			})
			return
		}
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", data)
	}
}
