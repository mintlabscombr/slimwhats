package handlers

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed swagger_ui.html
var swaggerHTML []byte

//go:embed all:openapi.yaml
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
		data, err := specFS.ReadFile("openapi.yaml")
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
