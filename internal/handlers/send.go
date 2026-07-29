package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/mauroneto/whatsmeow-api/internal/send"
)

// TextRequestBody is the JSON body for POST /api/v1/messages/text.
type TextRequestBody struct {
	To              string            `json:"to"`
	Body            string            `json:"body" binding:"required"`
	ID              string            `json:"id,omitempty"`
	Delay           int               `json:"delay,omitempty"`
	Mentions        []string          `json:"mentions,omitempty"`
	MentionAll      bool              `json:"mention_all,omitempty"`
	Quoted          *QuotedBody       `json:"quoted,omitempty"`
	ForwardingScore *uint32           `json:"forwarding_score,omitempty"`
	FormatJID       *bool             `json:"format_jid,omitempty"`
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
		cliV, _ := c.Get("client")
		cli, _ := cliV.(interface {
			// narrowed in main
		})
		_ = cli
		var body TextRequestBody
		if err := c.ShouldBind(&body); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_request",
				"message": err.Error(),
			})
			return
		}
		// We need the whatsmeow client from the context — cast it.
		// (The middleware sets "client" to *whatsmeow.Client.)
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
	To      string                 `json:"to"`
	Body    string                 `json:"body"`
	Header  string                 `json:"header,omitempty"`
	Footer  string                 `json:"footer,omitempty"`
	Buttons []send.ButtonRequest   `json:"buttons" binding:"required"`
	ID      string                 `json:"id,omitempty"`
	Delay   int                    `json:"delay,omitempty"`
}

// SendButtonsHandler handles POST /api/v1/messages/buttons.
func SendButtonsHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		whatsCli := extractClient(c)
		if whatsCli == nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "client_unavailable",
				"message": "instance client not loaded",
			})
			return
		}
		var body ButtonsRequestBody
		if err := c.ShouldBind(&body); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_request",
				"message": err.Error(),
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
