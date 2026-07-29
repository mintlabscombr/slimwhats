// Package webhook also contains the event normalizer: it takes whatsmeow
// events and converts them to the normalized Event envelope that the
// dispatcher POSTs to the operator's webhook URL.
package webhook

import (
	"encoding/json"
	"strings"
	"time"

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
		return Event{
			Event:      "message.received",
			InstanceID: instanceID,
			Timestamp:  ts,
			Data: map[string]interface{}{
				"id":        e.Info.ID,
				"from":      e.Info.Sender.String(),
				"chat":      e.Info.Chat.String(),
				"is_group":  e.Info.IsGroup,
				"type":      e.Info.Type,
				"timestamp": e.Info.Timestamp.UTC().Format(time.RFC3339),
			},
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
