package handlers

import (
	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow"
)

// extractClient pulls the *whatsmeow.Client that the InstanceAPIKeyAuth
// middleware stashed on the context. Returns nil if it's missing or the
// wrong type.
func extractClient(c *gin.Context) *whatsmeow.Client {
	v, ok := c.Get("client")
	if !ok {
		return nil
	}
	cli, _ := v.(*whatsmeow.Client)
	return cli
}

// extractInstance pulls the *instance.Instance stashed by the auth
// middleware.
func extractInstance(c *gin.Context) interface{} {
	v, ok := c.Get("instance")
	if !ok {
		return nil
	}
	return v
}
