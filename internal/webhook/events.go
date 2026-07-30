// Package webhook also contains the event normalizer: it takes whatsmeow
// events and converts them to the normalized Event envelope that the
// dispatcher POSTs to the operator's webhook URL.
package webhook

import (
	"encoding/json"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// Normalize converts a whatsmeow Event into our normalized Event
// envelope. Returns (event, true) on success, (zero, false) if the
// event type is not one we currently surface (the dispatcher will skip).
func Normalize(instanceID string, raw interface{}) (Event, bool) {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	switch e := raw.(type) {
	case *events.Message:
		data := map[string]interface{}{
			"id":        e.Info.ID,
			"from":      e.Info.Sender.String(),
			"chat":      e.Info.Chat.String(),
			"is_group":  e.Info.IsGroup,
			"type":      e.Info.Type,
			"timestamp": e.Info.Timestamp.UTC().Format(time.RFC3339),
			"from_me":   e.Info.IsFromMe,
		}
		if e.Info.PushName != "" {
			data["push_name"] = e.Info.PushName
		}
		// Extract the actual content: text body, media metadata,
		// location, contact, quoted (replies), mentions, reactions,
		// button replies, polls. See extractMessageContent for the
		// per-type mapping.
		for k, v := range extractMessageContent(e.Message) {
			data[k] = v
		}
		return Event{
			Event:      "message.received",
			InstanceID: instanceID,
			Timestamp:  ts,
			Data:       data,
		}, true
	case *events.Receipt:
		return Event{
			Event:      "message." + strings.ToLower(string(e.Type)),
			InstanceID: instanceID,
			Timestamp:  ts,
			Data: map[string]interface{}{
				"message_ids": messageIDsToStrings(e.MessageIDs),
				"chat":        e.Chat.String(),
				"sender":      e.Sender.String(),
				"timestamp":   e.Timestamp.UTC().Format(time.RFC3339),
			},
		}, true
	case *events.Connected:
		return Event{
			Event: "instance.connected", InstanceID: instanceID, Timestamp: ts,
			Data: map[string]interface{}{},
		}, true
	case *events.Disconnected:
		return Event{
			Event: "instance.disconnected", InstanceID: instanceID, Timestamp: ts,
			Data: map[string]interface{}{},
		}, true
	case *events.LoggedOut:
		return Event{
			Event: "instance.logged_out", InstanceID: instanceID, Timestamp: ts,
			Data: map[string]interface{}{"reason": e.Reason.String()},
		}, true
	case *events.PairSuccess:
		return Event{
			Event: "instance.pair_success", InstanceID: instanceID, Timestamp: ts,
			Data: map[string]interface{}{
				"phone": e.ID.User,
				"jid":   e.ID.String(),
				"lid":   e.LID.String(),
			},
		}, true
	case *events.Contact:
		return Event{
			Event: "contact.changed", InstanceID: instanceID, Timestamp: ts,
			Data: map[string]interface{}{
				"jid":       e.JID.String(),
				"timestamp": e.Timestamp.UTC().Format(time.RFC3339),
			},
		}, true
	case *events.GroupInfo:
		return Event{
			Event: "group.changed", InstanceID: instanceID, Timestamp: ts,
			Data: map[string]interface{}{
				"jid":       e.JID.String(),
				"name":      e.Name,
				"timestamp": e.Timestamp.UTC().Format(time.RFC3339),
			},
		}, true
	case *events.Presence:
		return Event{
			Event: "presence.updated", InstanceID: instanceID, Timestamp: ts,
			Data: map[string]interface{}{
				"from":      e.From.String(),
				"available": !e.Unavailable,
				"last_seen": fmtTimestamp(e.LastSeen),
			},
		}, true
	}
	return Event{}, false
}

func messageIDsToStrings(ids []types.MessageID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

func fmtTimestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// MustMarshal is a tiny helper for tests and callers that know the
// payload is serializable.
func MustMarshal(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}

// extractMessageContent walks a whatsmeow Message protobuf and
// pulls out the user-facing fields that operators care about:
// the text body, media metadata (mimetype, file size, filename,
// duration), location coordinates, contact card, replies (the
// quoted message), mentions, reactions, button replies, polls,
// and live location. The returned map is meant to be merged into
// the data section of a message.received webhook event.
//
// Fields are only included when meaningful — e.g. `body` only
// appears if the message has text or a caption, `media` only if
// the message is an attachment. We do NOT include the raw media
// bytes (operators fetch those via the API if needed) and we
// skip protocol/control messages (security notification, app
// state sync, history sync notification) — the dispatcher
// never reaches us for those because the subscriber filters
// them out before normalization.
func extractMessageContent(m *waE2E.Message) map[string]interface{} {
	out := map[string]interface{}{}
	if m == nil {
		// No body at the message level (shouldn't happen for
		// *events.Message, but defensive). Nothing to extract.
		return out
	}

	switch {
	case m.Conversation != nil:
		// Plain text message (most common case).
		setIfNotEmpty(out, "body", m.GetConversation())

	case m.ExtendedTextMessage != nil:
		// Extended text: links, mentions, formatted text. The text
		// is in .Text, the original quoted in .ContextInfo.
		setIfNotEmpty(out, "body", m.ExtendedTextMessage.GetText())

	case m.ImageMessage != nil:
		extractMedia(out, m.ImageMessage.GetCaption(),
			m.ImageMessage.GetMimetype(), m.ImageMessage.GetFileLength(), 0, "")

	case m.VideoMessage != nil:
		extractMedia(out, m.VideoMessage.GetCaption(),
			m.VideoMessage.GetMimetype(), m.VideoMessage.GetFileLength(),
			int(m.VideoMessage.GetSeconds()), "")

	case m.AudioMessage != nil:
		// Audio usually has no caption; ptt=true means voice note.
		media := map[string]interface{}{
			"mimetype":  m.AudioMessage.GetMimetype(),
			"file_size": m.AudioMessage.GetFileLength(),
			"seconds":   int(m.AudioMessage.GetSeconds()),
			"ptt":       m.AudioMessage.GetPTT(),
		}
		out["media"] = media

	case m.DocumentMessage != nil:
		extractMedia(out, m.DocumentMessage.GetCaption(),
			m.DocumentMessage.GetMimetype(), m.DocumentMessage.GetFileLength(), 0,
			m.DocumentMessage.GetFileName())

	case m.StickerMessage != nil:
		// Stickers have no caption, but we surface the mimetype
		// and size so consumers can render a placeholder + fetch
		// the actual sticker via API if needed.
		out["media"] = map[string]interface{}{
			"mimetype":  m.StickerMessage.GetMimetype(),
			"file_size": m.StickerMessage.GetFileLength(),
			"animated":  m.StickerMessage.GetIsAnimated(),
		}

	case m.LocationMessage != nil:
		out["location"] = map[string]interface{}{
			"latitude":  m.LocationMessage.GetDegreesLatitude(),
			"longitude": m.LocationMessage.GetDegreesLongitude(),
			"name":      m.LocationMessage.GetName(),
			"address":   m.LocationMessage.GetAddress(),
		}

	case m.LiveLocationMessage != nil:
		// Live location shares lat/lon with static location, plus
		// a sequence number for the freshness window. The Name/Address
		// fields don't exist on the live variant — only the caption
		// if the user added one.
		out["location"] = map[string]interface{}{
			"latitude":        m.LiveLocationMessage.GetDegreesLatitude(),
			"longitude":       m.LiveLocationMessage.GetDegreesLongitude(),
			"caption":         m.LiveLocationMessage.GetCaption(),
			"live":            true,
			"sequence_number": m.LiveLocationMessage.GetSequenceNumber(),
			"accuracy_m":      m.LiveLocationMessage.GetAccuracyInMeters(),
		}

	case m.ContactMessage != nil:
		out["contact"] = map[string]interface{}{
			"display_name": m.ContactMessage.GetDisplayName(),
			"vcard":        m.ContactMessage.GetVcard(),
		}

	case m.ButtonsResponseMessage != nil:
		// User tapped a quick-reply button. Surface the selected
		// id + display text so consumers can route on it.
		out["button_reply"] = map[string]interface{}{
			"id":           m.ButtonsResponseMessage.GetSelectedButtonID(),
			"display_text": m.ButtonsResponseMessage.GetSelectedDisplayText(),
		}

	case m.TemplateButtonReplyMessage != nil:
		// Same idea but for the legacy template-button variant.
		out["button_reply"] = map[string]interface{}{
			"id":           m.TemplateButtonReplyMessage.GetSelectedID(),
			"display_text": m.TemplateButtonReplyMessage.GetSelectedDisplayText(),
			"index":        m.TemplateButtonReplyMessage.GetSelectedIndex(),
		}

	case m.ReactionMessage != nil:
		out["reaction"] = map[string]interface{}{
			"emoji":     m.ReactionMessage.GetText(),
			"target_id": m.ReactionMessage.GetKey().GetID(),
		}

	case m.PollCreationMessage != nil:
		opts := m.PollCreationMessage.GetOptions()
		optStrs := make([]string, 0, len(opts))
		for _, o := range opts {
			optStrs = append(optStrs, o.GetOptionName())
		}
		out["poll"] = map[string]interface{}{
			"name":    m.PollCreationMessage.GetName(),
			"options": optStrs,
			"action":  "creation",
		}

	case m.PollUpdateMessage != nil:
		// PollUpdateMessage carries an encrypted vote payload
		// (PollEncValue). The library doesn't expose the
		// decrypted options directly; we surface the
		// sender timestamp + poll key so the consumer can correlate
		// to the original PollCreationMessage event.
		if key := m.PollUpdateMessage.GetPollCreationMessageKey(); key != nil {
			out["poll"] = map[string]interface{}{
				"target_poll_id": key.GetID(),
				"action":         "vote",
				"sender_ts":      m.PollUpdateMessage.GetSenderTimestampMS(),
			}
		}
	}

	// Mentions + quoted live on the inner message's ContextInfo,
	// which is a different type than MessageContextInfo. Each
	// body case exposes GetContextInfo() returning the same
	// *ContextInfo. Switch again here so we don't miss mentions
	// in a non-text message.
	extractContextExtras(out, findBodyContextInfo(m))
	return out
}

// findBodyContextInfo returns the *ContextInfo attached to the
// populated body type, or nil if the body has no ContextInfo
// (rare — e.g. plain text or unsupported variants). Used to
// surface mentions and quoted messages, which live on the body
// wrapper rather than the top-level Message.
func findBodyContextInfo(m *waE2E.Message) *waE2E.ContextInfo {
	switch {
	case m.ExtendedTextMessage != nil:
		return m.ExtendedTextMessage.GetContextInfo()
	case m.ImageMessage != nil:
		return m.ImageMessage.GetContextInfo()
	case m.VideoMessage != nil:
		return m.VideoMessage.GetContextInfo()
	case m.AudioMessage != nil:
		return m.AudioMessage.GetContextInfo()
	case m.DocumentMessage != nil:
		return m.DocumentMessage.GetContextInfo()
	case m.StickerMessage != nil:
		return m.StickerMessage.GetContextInfo()
	case m.LocationMessage != nil:
		return m.LocationMessage.GetContextInfo()
	case m.ContactMessage != nil:
		return m.ContactMessage.GetContextInfo()
	case m.ButtonsResponseMessage != nil:
		return m.ButtonsResponseMessage.GetContextInfo()
	case m.TemplateButtonReplyMessage != nil:
		return m.TemplateButtonReplyMessage.GetContextInfo()
	case m.ReactionMessage != nil:
		// ReactionMessage doesn't carry its own ContextInfo;
		// the target message key + emoji are the only fields.
		return nil
	case m.PollCreationMessage != nil:
		return m.PollCreationMessage.GetContextInfo()
	case m.PollUpdateMessage != nil:
		// PollUpdateMessage doesn't expose ContextInfo.
		return nil
	}
	return nil
}

// extractMedia populates the standard "media" sub-object shared by
// image/video/document. caption may be empty for media-without-text.
func extractMedia(out map[string]interface{}, caption, mimetype string, fileLength uint64, seconds int, fileName string) {
	media := map[string]interface{}{
		"mimetype":  mimetype,
		"file_size": fileLength,
	}
	if caption != "" {
		out["body"] = caption
	}
	if seconds > 0 {
		media["seconds"] = seconds
	}
	if fileName != "" {
		media["file_name"] = fileName
	}
	out["media"] = media
}

// extractContextExtras pulls mentions and the quoted message from
// the ContextInfo. Called for every message type since these
// fields are independent of the body type.
func extractContextExtras(out map[string]interface{}, ctx *waE2E.ContextInfo) {
	if ctx == nil {
		return
	}
	if mentions := ctx.GetMentionedJID(); len(mentions) > 0 {
		// GetMentionedJID already returns []string.
		out["mentions"] = mentions
	}
	if q := ctx.GetQuotedMessage(); q != nil {
		quoted := map[string]interface{}{
			"id":     ctx.GetStanzaID(),
			"sender": ctx.GetParticipant(),
		}
		// Recursively extract the quoted message body, so a reply
		// to a text message carries the original text, a reply to
		// an image carries the image caption, etc.
		for k, v := range extractMessageContent(q) {
			if k == "from_me" || k == "push_name" {
				continue
			}
			quoted[k] = v
		}
		out["quoted"] = quoted
	}
}

// setIfNotEmpty sets a key only when the value is non-empty — keeps
// the webhook payload free of empty strings / zero values.
func setIfNotEmpty(out map[string]interface{}, key, value string) {
	if value != "" {
		out[key] = value
	}
}
