package send

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// TextRequest is the input to SendText.
type TextRequest struct {
	To              string
	Body            string
	ID              string // optional client-supplied message ID
	Delay           int    // ms, 0..30000
	Mentions        []string
	MentionAll      bool
	Quoted          *QuotedMessage
	ForwardingScore *uint32
	FormatJID       *bool
	DisableWA       bool
}

// QuotedMessage is the reply-to metadata.
type QuotedMessage struct {
	MessageID   string
	Participant string
}

// SendResponse is the API response shape (FR-13d).
type SendResponse struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	ServerID  int    `json:"server_id"`
	Chat      string `json:"chat"`
	IsGroup   bool   `json:"is_group"`
	Type      string `json:"type"`
}

// SendText implements the FR-13 orchestration: resolve → typing sim →
// retry → build ExtendedTextMessage → SendMessage. Returns the
// clean FR-13d response shape.
func SendText(ctx context.Context, client *whatsmeow.Client, req TextRequest) (*SendResponse, error) {
	if req.Body == "" {
		return nil, errors.New("body is required")
	}
	if len(req.Body) > 65536 {
		return nil, errors.New("body too long (>65536 chars)")
	}
	if req.Delay < 0 || req.Delay > 30000 {
		return nil, errors.New("delay must be 0..30000 ms")
	}

	recipient, err := Resolve(ctx, client, RecipInput{
		Number:    req.To,
		FormatJID: req.FormatJID,
		DisableWA: req.DisableWA,
	})
	if err != nil {
		return nil, err
	}

	// Typing simulation
	if req.Delay > 0 {
		_ = client.SendChatPresence(ctx, recipient, types.ChatPresenceComposing, types.ChatPresenceMediaText)
		time.Sleep(time.Duration(req.Delay) * time.Millisecond)
		_ = client.SendChatPresence(ctx, recipient, types.ChatPresencePaused, types.ChatPresenceMediaText)
	}

	// Build the ExtendedTextMessage
	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(req.Body),
		},
	}

	// Apply ContextInfo
	ci := buildContextInfo(req.Quoted, req.Mentions, req.MentionAll, client, recipient, ctx)
	if ci != nil {
		msg.ExtendedTextMessage.ContextInfo = ci
	}

	// Message ID
	msgID := req.ID
	if msgID == "" {
		msgID = client.GenerateMessageID()
	}

	sendExtra := whatsmeow.SendRequestExtra{ID: msgID}

	// Retry on client disconnected
	var response whatsmeow.SendResponse
	for attempt := 1; attempt <= 3; attempt++ {
		response, err = client.SendMessage(ctx, recipient, msg, sendExtra)
		if err == nil {
			break
		}
		msg2 := err.Error()
		if !strings.Contains(msg2, "client disconnected") && !strings.Contains(msg2, "no active session") {
			return nil, fmt.Errorf("send: %w", err)
		}
		if attempt == 3 {
			return nil, fmt.Errorf("send failed after 3 attempts: %w", err)
		}
		time.Sleep(time.Duration(attempt) * time.Second)
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	if !response.Timestamp.IsZero() {
		ts = response.Timestamp.UTC().Format(time.RFC3339)
	}
	return &SendResponse{
		ID:        msgID,
		Timestamp: ts,
		ServerID:  int(response.ServerID),
		Chat:      recipient.String(),
		IsGroup:   recipient.Server == "g.us",
		Type:      "ExtendedTextMessage",
	}, nil
}

func buildContextInfo(q *QuotedMessage, mentions []string, mentionAll bool, client *whatsmeow.Client, recipient types.JID, ctx context.Context) *waE2E.ContextInfo {
	ci := &waE2E.ContextInfo{}
	hasAny := false
	if q != nil {
		ci.StanzaID = proto.String(q.MessageID)
		ci.Participant = proto.String(q.Participant)
		ci.QuotedMessage = &waE2E.Message{Conversation: proto.String("")}
		hasAny = true
	}
	if mentionAll && recipient.Server == "g.us" {
		// Resolve every participant of the group
		gi, err := client.GetGroupInfo(ctx, recipient)
		if err == nil {
			for _, p := range gi.Participants {
				mentions = append(mentions, p.JID.String())
			}
		}
	}
	if len(mentions) > 0 && recipient.Server == "g.us" {
		ci.MentionedJID = mentions
		hasAny = true
	}
	if !hasAny {
		return nil
	}
	return ci
}
