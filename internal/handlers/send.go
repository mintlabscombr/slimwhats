// Package handlers — outbound message handlers (US-012, US-013, US-022).
//
// Both /api/v1/messages/text and /api/v1/messages/buttons share:
//   - InstanceAPIKeyAuth middleware upstream (sets "client" + "instance")
//   - Per-JID outbound rate limit (US-022: 20 msgs / 60s default → 429 with Retry-After)
//   - The same send-orchestration pipeline inside the send package
package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/mauroneto/whatsmeow-api/internal/send"
)

// perJIDLimiter is shared across the package; created in main.go and
// passed in via SetPerJIDLimiter. Defaults to a fresh 20 / 60s limiter.
var perJIDLimiter = send.NewPerJIDRateLimiter()

// SetPerJIDLimiter replaces the default per-JID limiter. Call from main.
func SetPerJIDLimiter(l *send.PerJIDRateLimiter) {
	if l != nil {
		perJIDLimiter = l
	}
}

// rateLimitOrAbort applies the per-JID outbound rate limit (US-022).
// On limit, sets the Retry-After header and aborts with 429. Returns
// true when the request may proceed.
func rateLimitOrAbort(c *gin.Context, jid string) bool {
	ok, retry := perJIDLimiter.Check(jid)
	if ok {
		return true
	}
	c.Header("Retry-After", formatSeconds(retry))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error":   "rate_limited",
		"message": "too many sends to this JID; slow down",
	})
	return false
}

// TextRequestBody is the JSON body for POST /api/v1/messages/text.
type TextRequestBody struct {
	To              string      `json:"to"`
	Body            string      `json:"body" binding:"required"`
	ID              string      `json:"id,omitempty"`
	Delay           int         `json:"delay,omitempty"`
	Mentions        []string    `json:"mentions,omitempty"`
	MentionAll      bool        `json:"mention_all,omitempty"`
	Quoted          *QuotedBody `json:"quoted,omitempty"`
	ForwardingScore *uint32     `json:"forwarding_score,omitempty"`
	FormatJID       *bool       `json:"format_jid,omitempty"`
}

// QuotedBody is the reply-to metadata in the request body.
type QuotedBody struct {
	MessageID   string `json:"message_id"`
	Participant string `json:"participant"`
}

// SendTextHandler handles POST /api/v1/messages/text. Requires
// InstanceAPIKeyAuth middleware upstream.
func SendTextHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var body TextRequestBody
		if err := c.ShouldBind(&body); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_request",
				"message": err.Error(),
			})
			return
		}
		// Per-JID rate limit (US-022)
		if !rateLimitOrAbort(c, body.To) {
			return
		}
		whatsCli := extractClient(c)
		if whatsCli == nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "client_unavailable",
				"message": "instance client not loaded",
			})
			return
		}
		var quoted *send.QuotedMessage
		if body.Quoted != nil {
			quoted = &send.QuotedMessage{
				MessageID:   body.Quoted.MessageID,
				Participant: body.Quoted.Participant,
			}
		}
		resp, err := send.SendText(c.Request.Context(), whatsCli, send.TextRequest{
			To:              body.To,
			Body:            body.Body,
			ID:              body.ID,
			Delay:           body.Delay,
			Mentions:        body.Mentions,
			MentionAll:      body.MentionAll,
			Quoted:          quoted,
			ForwardingScore: body.ForwardingScore,
			FormatJID:       body.FormatJID,
		})
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "send_failed",
				"message": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, resp)
	}
}

// ButtonsRequestBody is the JSON body for POST /api/v1/messages/buttons.
type ButtonsRequestBody struct {
	To      string               `json:"to"`
	Body    string               `json:"body"`
	Header  string               `json:"header,omitempty"`
	Footer  string               `json:"footer,omitempty"`
	Buttons []send.ButtonRequest `json:"buttons" binding:"required"`
	ID      string               `json:"id,omitempty"`
	Delay   int                  `json:"delay,omitempty"`
}

// SendButtonsHandler handles POST /api/v1/messages/buttons.
func SendButtonsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		var body ButtonsRequestBody
		if err := c.ShouldBind(&body); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_request",
				"message": err.Error(),
			})
			return
		}
		// Per-JID rate limit (US-022)
		if !rateLimitOrAbort(c, body.To) {
			return
		}
		whatsCli := extractClient(c)
		if whatsCli == nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "client_unavailable",
				"message": "instance client not loaded",
			})
			return
		}
		resp, err := send.SendButtons(c.Request.Context(), whatsCli, send.ButtonsRequest{
			To:      body.To,
			Body:    body.Body,
			Header:  body.Header,
			Footer:  body.Footer,
			Buttons: body.Buttons,
			ID:      body.ID,
			Delay:   body.Delay,
		})
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "send_failed",
				"message": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, resp)
	}
}
