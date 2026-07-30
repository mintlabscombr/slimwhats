package webhook

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// TestNormalize_TextBody ensures the body field appears for a plain
// text message and that no empty fields leak in.
func TestNormalize_TextBody(t *testing.T) {
	jid := types.NewJID("5511999999999", types.DefaultUserServer)
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Sender:  jid,
			Chat:    jid,
			IsFromMe: false,
			IsGroup:  false,
		},
		ID:        "3EB0TEXT",
		Type:      "text",
		PushName:  "Aura",
		Timestamp: time.Now(),
	}
	evt := &events.Message{
		Info:    *info,
		Message: &waE2E.Message{Conversation: stringPtr("olá, isso é um teste")},
	}
	e, ok := Normalize("inst-1", evt)
	if !ok {
		t.Fatal("expected ok=true")
	}
	dump(t, e)
	if e.Data["body"] != "olá, isso é um teste" {
		t.Errorf("body mismatch: %v", e.Data["body"])
	}
	if e.Data["from_me"] != false {
		t.Errorf("from_me mismatch: %v", e.Data["from_me"])
	}
	if e.Data["push_name"] != "Aura" {
		t.Errorf("push_name mismatch: %v", e.Data["push_name"])
	}
	if e.Data["type"] != "text" {
		t.Errorf("type mismatch: %v", e.Data["type"])
	}
	if _, has := e.Data["media"]; has {
		t.Errorf("media should not be set for text")
	}
	if _, has := e.Data["quoted"]; has {
		t.Errorf("quoted should not be set when not a reply")
	}
}

// TestNormalize_ImageWithCaption ensures the body becomes the
// caption and a media sub-object appears.
func TestNormalize_ImageWithCaption(t *testing.T) {
	jid := types.NewJID("5511999999999", types.DefaultUserServer)
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Sender:  jid,
			Chat:    jid,
			IsFromMe: false,
			IsGroup:  false,
		},
		ID:        "3EB0IMG",
		Type:      "image",
		PushName:  "Aura",
		Timestamp: time.Now(),
	}
	evt := &events.Message{
		Info: *info,
		Message: &waE2E.Message{
			ImageMessage: &waE2E.ImageMessage{
				Caption:    stringPtr("olha essa foto"),
				Mimetype:   stringPtr("image/jpeg"),
				FileLength: uint64Ptr(234567),
			},
		},
	}
	e, ok := Normalize("inst-1", evt)
	if !ok {
		t.Fatal("expected ok=true")
	}
	dump(t, e)
	if e.Data["body"] != "olha essa foto" {
		t.Errorf("body (caption) mismatch: %v", e.Data["body"])
	}
	media, ok := e.Data["media"].(map[string]interface{})
	if !ok {
		t.Fatalf("media missing or wrong type")
	}
	if media["mimetype"] != "image/jpeg" {
		t.Errorf("media.mimetype: %v", media["mimetype"])
	}
	if media["file_size"] != uint64(234567) {
		t.Errorf("media.file_size: %v", media["file_size"])
	}
}

// TestNormalize_ReplyWithQuote ensures the quoted field is
// populated with the original message's body, recursively.
func TestNormalize_ReplyWithQuote(t *testing.T) {
	jid := types.NewJID("5511999999999", types.DefaultUserServer)
	// The original message being replied to (an extended text).
	quoted := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: stringPtr("mensagem original"),
		},
	}
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Sender:  jid,
			Chat:    jid,
			IsFromMe: true,
			IsGroup:  false,
		},
		ID:        "3EB0REPLY",
		Type:      "text",
		PushName:  "",
		Timestamp: time.Now(),
	}
	evt := &events.Message{
		Info: *info,
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: stringPtr("minha resposta"),
				ContextInfo: &waE2E.ContextInfo{
					StanzaID:      stringPtr("3EB0ORIG"),
					Participant:   stringPtr("5511999999999@s.whatsapp.net"),
					QuotedMessage: quoted,
				},
			},
		},
	}
	e, ok := Normalize("inst-1", evt)
	if !ok {
		t.Fatal("expected ok=true")
	}
	dump(t, e)
	if e.Data["from_me"] != true {
		t.Errorf("from_me should be true, got: %v", e.Data["from_me"])
	}
	if e.Data["body"] != "minha resposta" {
		t.Errorf("body mismatch: %v", e.Data["body"])
	}
	quotedMap, ok := e.Data["quoted"].(map[string]interface{})
	if !ok {
		t.Fatalf("quoted missing or wrong type: %#v", e.Data["quoted"])
	}
	if quotedMap["body"] != "mensagem original" {
		t.Errorf("quoted.body: %v", quotedMap["body"])
	}
	if quotedMap["id"] != "3EB0ORIG" {
		t.Errorf("quoted.id: %v", quotedMap["id"])
	}
	if quotedMap["sender"] != "5511999999999@s.whatsapp.net" {
		t.Errorf("quoted.sender: %v", quotedMap["sender"])
	}
	// from_me/push_name should NOT propagate into quoted (it's
	// the original sender's info, not ours)
	if _, has := quotedMap["from_me"]; has {
		t.Errorf("quoted should not include from_me")
	}
	if _, has := quotedMap["push_name"]; has {
		t.Errorf("quoted should not include push_name")
	}
}

// TestNormalize_ButtonReply ensures user taps on quick-reply
// buttons surface the id + display text.
func TestNormalize_ButtonReply(t *testing.T) {
	jid := types.NewJID("5511999999999", types.DefaultUserServer)
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Sender:  jid,
			Chat:    jid,
			IsFromMe: false,
			IsGroup:  false,
		},
		ID:        "3EB0BTN",
		Type:      "buttons_response",
		PushName:  "Aura",
		Timestamp: time.Now(),
	}
	evt := &events.Message{
		Info: *info,
		Message: &waE2E.Message{
			ButtonsResponseMessage: &waE2E.ButtonsResponseMessage{
				SelectedButtonID: stringPtr("yes"),
				Response: &waE2E.ButtonsResponseMessage_SelectedDisplayText{
					SelectedDisplayText: "Yes",
				},
			},
		},
	}
	e, ok := Normalize("inst-1", evt)
	if !ok {
		t.Fatal("expected ok=true")
	}
	dump(t, e)
	br, ok := e.Data["button_reply"].(map[string]interface{})
	if !ok {
		t.Fatalf("button_reply missing: %#v", e.Data["button_reply"])
	}
	if br["id"] != "yes" {
		t.Errorf("button_reply.id: %v", br["id"])
	}
	if br["display_text"] != "Yes" {
		t.Errorf("button_reply.display_text: %v", br["display_text"])
	}
}

// TestNormalize_Reaction ensures reactions surface the emoji
// and the target message ID.
func TestNormalize_Reaction(t *testing.T) {
	jid := types.NewJID("5511999999999", types.DefaultUserServer)
	info := &types.MessageInfo{
		MessageSource: types.MessageSource{
			Sender:  jid,
			Chat:    jid,
			IsFromMe: false,
			IsGroup:  false,
		},
		ID:        "3EB0RX",
		Type:      "reaction",
		PushName:  "Aura",
		Timestamp: time.Now(),
	}
	evt := &events.Message{
		Info: *info,
		Message: &waE2E.Message{
			ReactionMessage: &waE2E.ReactionMessage{
				Text: stringPtr("❤️"),
				Key:  &waCommon.MessageKey{ID: stringPtr("3EB0TARGET")},
			},
		},
	}
	e, ok := Normalize("inst-1", evt)
	if !ok {
		t.Fatal("expected ok=true")
	}
	dump(t, e)
	r, ok := e.Data["reaction"].(map[string]interface{})
	if !ok {
		t.Fatalf("reaction missing: %#v", e.Data["reaction"])
	}
	if r["emoji"] != "❤️" {
		t.Errorf("reaction.emoji: %v", r["emoji"])
	}
	if r["target_id"] != "3EB0TARGET" {
		t.Errorf("reaction.target_id: %v", r["target_id"])
	}
}

func dump(t *testing.T, e Event) {
	b, _ := json.MarshalIndent(e, "", "  ")
	t.Logf("payload:\n%s", string(b))
}

func stringPtr(s string) *string  { return &s }
func uint64Ptr(u uint64) *uint64   { return &u }
func uint32Ptr(u uint32) *uint32   { return &u }

// Avoid unused-import warning if some helpers are not all used.
var _ = fmt.Sprintf
