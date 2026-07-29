package send

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/binary"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
)

// ButtonRequest is one button in the buttons array.
type ButtonRequest struct {
	Type        string `json:"type"`        // reply, url, copy, call, pix
	ID          string `json:"id,omitempty"`
	Label       string `json:"label,omitempty"`
	URL         string `json:"url,omitempty"`
	Code        string `json:"code,omitempty"`
	PhoneNumber string `json:"phone_number,omitempty"`

	// Pix-specific (server-built payment_info button)
	Currency     string `json:"currency,omitempty"`
	Name         string `json:"name,omitempty"`
	KeyType      string `json:"key_type,omitempty"`
	Key          string `json:"key,omitempty"`
	AmountCents  int64  `json:"amount_cents,omitempty"`
	TxID         string `json:"txid,omitempty"`
	MerchantName string `json:"merchant_name,omitempty"`
	MerchantCity string `json:"merchant_city,omitempty"`
}

// ButtonsRequest is the input to SendButtons.
type ButtonsRequest struct {
	To      string
	Body    string
	Header  string
	Footer  string
	Buttons []ButtonRequest

	// Shared orchestration (FR-13 a/b/c/d)
	ID              string
	Delay           int
	Quoted          *QuotedMessage
	Mentions        []string
	MentionAll      bool
	ForwardingScore *uint32
	FormatJID       *bool
	DisableWA       bool
}

// SendButtons handles the multi-type button composition: reply uses the
// legacy ButtonsMessage envelope; url/copy/call/pix use the NativeFlowMessage
// envelope with the per-type ButtonParamsJSON.
func SendButtons(ctx context.Context, client *whatsmeow.Client, req ButtonsRequest) (*SendResponse, error) {
	if len(req.Buttons) == 0 {
		return nil, errors.New("at least one button is required")
	}
	if err := validateButtons(req); err != nil {
		return nil, err
	}

	recipient, err := Resolve(ctx, client, RecipInput{
		Number:    req.To,
		FormatJID: req.FormatJID,
		DisableWA: req.DisableWA,
	})
	if err != nil {
		return nil, err
	}

	if req.Delay > 0 {
		_ = client.SendChatPresence(ctx, recipient, types.ChatPresenceComposing, types.ChatPresenceMediaText)
		time.Sleep(time.Duration(req.Delay) * time.Millisecond)
		_ = client.SendChatPresence(ctx, recipient, types.ChatPresencePaused, types.ChatPresenceMediaText)
	}

	btnMsgSecret, err := newMessageSecret()
	if err != nil {
		return nil, fmt.Errorf("message secret: %w", err)
	}

	msg, extra, err := buildButtonsMessage(req, recipient, btnMsgSecret)
	if err != nil {
		return nil, err
	}

	msgID := req.ID
	if msgID == "" {
		msgID = client.GenerateMessageID()
	}
	extra.ID = msgID

	var response whatsmeow.SendResponse
	for attempt := 1; attempt <= 3; attempt++ {
		response, err = client.SendMessage(ctx, recipient, msg, extra)
		if err == nil {
			break
		}
		m := err.Error()
		if !strings.Contains(m, "client disconnected") && !strings.Contains(m, "no active session") {
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
		Type:      buttonsResponseType(req.Buttons[0].Type),
	}, nil
}

func validateButtons(req ButtonsRequest) error {
	n := len(req.Buttons)
	if n > 3 {
		return errors.New("buttons array may have at most 3 entries")
	}
	hasReply := false
	hasPix := false
	hasCopy := false
	hasCall := false
	hasURL := false
	for _, b := range req.Buttons {
		switch b.Type {
		case "reply":
			hasReply = true
			if b.ID == "" {
				return errors.New("reply button requires id")
			}
			if len(b.ID) > 256 {
				return errors.New("reply.id must be ≤ 256 chars")
			}
			if len(b.Label) > 20 {
				return errors.New("reply.label must be ≤ 20 chars")
			}
		case "url":
			hasURL = true
			if len(b.URL) > 2048 || !(strings.HasPrefix(b.URL, "https://") || strings.HasPrefix(b.URL, "http://localhost") || strings.HasPrefix(b.URL, "http://127.0.0.1")) {
				return errors.New("url.url must be https:// (or http://localhost) and ≤ 2048 chars")
			}
			if len(b.Label) > 20 {
				return errors.New("url.label must be ≤ 20 chars")
			}
		case "copy":
			hasCopy = true
			if len(b.Code) > 256 {
				return errors.New("copy.code must be ≤ 256 chars")
			}
			if len(b.Label) > 20 {
				return errors.New("copy.label must be ≤ 20 chars")
			}
		case "call":
			hasCall = true
			if !phoneRegexp.MatchString(b.PhoneNumber) {
				return errors.New("call.phone_number must be valid E.164")
			}
			if len(b.Label) > 20 {
				return errors.New("call.label must be ≤ 20 chars")
			}
		case "pix":
			hasPix = true
			if b.Currency != "BRL" {
				return errors.New("pix.currency must be BRL")
			}
			if len(b.Name) > 25 {
				return errors.New("pix.name must be ≤ 25 chars")
			}
			if !validPixKeyType(b.KeyType) {
				return errors.New("pix.key_type must be one of: phone, email, cpf, cnpj, random")
			}
			if err := validatePixKey(b.KeyType, b.Key); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown button type: %q", b.Type)
		}
	}
	if hasReply && (hasURL || hasPix || hasCopy || hasCall) {
		return errors.New("reply cannot be combined with other types")
	}
	if hasPix && n != 1 {
		return errors.New("pix must be the only button in the message")
	}
	if hasReply && n > 3 {
		return errors.New("reply array can have at most 3 entries")
	}
	return nil
}

var phoneRegexp = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

var emailRegexp = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func validatePixKey(keyType, key string) error {
	if key == "" {
		return errors.New("pix.key is required")
	}
	switch keyType {
	case "phone":
		if !phoneRegexp.MatchString(key) {
			return errors.New("pix.key with keyType=phone must be valid E.164")
		}
	case "email":
		if !emailRegexp.MatchString(key) {
			return errors.New("pix.key with keyType=email must be a valid email")
		}
	case "cpf":
		digits := digitsOnlyRe.ReplaceAllString(key, "")
		if len(digits) != 11 {
			return errors.New("pix.key with keyType=cpf must be 11 digits (Brazilian CPF)")
		}
	case "cnpj":
		digits := digitsOnlyRe.ReplaceAllString(key, "")
		if len(digits) != 14 {
			return errors.New("pix.key with keyType=cnpj must be 14 digits (Brazilian CNPJ)")
		}
	case "random":
		if len(key) != 32 {
			return errors.New("pix.key with keyType=random must be 32 hex chars (EVP)")
		}
	}
	return nil
}

func validPixKeyType(t string) bool {
	switch t {
	case "phone", "email", "cpf", "cnpj", "random":
		return true
	}
	return false
}

func buttonsResponseType(firstType string) string {
	if firstType == "reply" {
		return "ButtonsMessage"
	}
	return "InteractiveMessage"
}

// buildButtonsMessage constructs the *waE2E.Message and the SendRequestExtra
// (with biz/bot XML AdditionalNodes) for the button composition.
func buildButtonsMessage(req ButtonsRequest, recipient types.JID, msgSecret []byte) (*waE2E.Message, whatsmeow.SendRequestExtra, error) {
	extra := whatsmeow.SendRequestExtra{}

	// Two paths: reply (ButtonsMessage) or non-reply (InteractiveMessage)
	if req.Buttons[0].Type == "reply" {
		return buildReplyButtonsMessage(req, msgSecret), extra, nil
	}
	bizName, addBot := bizNameForButtons(req.Buttons[0].Type)
	return buildNativeFlowMessage(req, recipient, msgSecret, bizName, addBot)
}

func bizNameForButtons(firstType string) (string, bool) {
	switch firstType {
	case "reply":
		return "quick_reply", true
	case "pix":
		return "payment_info", true
	default:
		return "mixed", true
	}
}

func buildReplyButtonsMessage(req ButtonsRequest, msgSecret []byte) *waE2E.Message {
	btns := make([]*waE2E.ButtonsMessage_Button, 0, len(req.Buttons))
	for _, b := range req.Buttons {
		btns = append(btns, &waE2E.ButtonsMessage_Button{
			ButtonID: proto.String(b.ID),
			ButtonText: &waE2E.ButtonsMessage_Button_ButtonText{
				DisplayText: proto.String(b.Label),
			},
			Type: waE2E.ButtonsMessage_Button_RESPONSE.Enum(),
		})
	}
	body := req.Body
	bm := &waE2E.ButtonsMessage{
		ContentText: proto.String(body),
		FooterText:  proto.String(req.Footer),
		HeaderType:  waE2E.ButtonsMessage_EMPTY.Enum(),
		Buttons:     btns,
	}
	return &waE2E.Message{
		DocumentWithCaptionMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{ButtonsMessage: bm},
		},
		MessageContextInfo: &waE2E.MessageContextInfo{
			MessageSecret: msgSecret,
		},
	}
}

func buildNativeFlowMessage(req ButtonsRequest, recipient types.JID, msgSecret []byte, bizName string, addBot bool) (*waE2E.Message, whatsmeow.SendRequestExtra, error) {
	nfb := make([]*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, 0, len(req.Buttons))
	for _, b := range req.Buttons {
		nb, err := buildNativeFlowButton(b)
		if err != nil {
			return nil, whatsmeow.SendRequestExtra{}, err
		}
		nfb = append(nfb, nb)
	}

	// messageParamsJSON: for url/copy/call it's {"from":"api","templateId":<ms>};
	// for pix it's {"native_flow_name":"order_details","version":1}
	var messageParamsJSON string
	if req.Buttons[0].Type == "pix" {
		messageParamsJSON = `{"native_flow_name":"order_details","version":1}`
	} else {
		ms := time.Now().UnixNano() / 1000000
		messageParamsJSON = fmt.Sprintf(`{"from":"api","templateId":%d}`, ms)
	}

	body := formatBody(req.Body, req.Header)
	im := &waE2E.InteractiveMessage{
		Body:   &waE2E.InteractiveMessage_Body{Text: proto.String(body)},
		Footer: &waE2E.InteractiveMessage_Footer{Text: proto.String(req.Footer)},
		InteractiveMessage: &waE2E.InteractiveMessage_NativeFlowMessage_{
			NativeFlowMessage: &waE2E.InteractiveMessage_NativeFlowMessage{
				Buttons:           nfb,
				MessageParamsJSON: proto.String(messageParamsJSON),
				MessageVersion:    proto.Int32(1),
			},
		},
	}
	msg := &waE2E.Message{
		DocumentWithCaptionMessage: &waE2E.FutureProofMessage{
			Message: &waE2E.Message{InteractiveMessage: im},
		},
		MessageContextInfo: &waE2E.MessageContextInfo{
			MessageSecret: msgSecret,
		},
	}

	// Build biz/bot XML nodes
	nodes := []binary.Node{*bizXMLNode(bizName)}
	if addBot && recipient.Server == "s.whatsapp.net" {
		nodes = append(nodes, *botXMLNode())
	}
	extra := whatsmeow.SendRequestExtra{
		AdditionalNodes: &nodes,
	}

	return msg, extra, nil
}

// bizXMLNode constructs <biz><interactive type="native_flow" v="1"><native_flow name="..."/></interactive></biz>.
func bizXMLNode(name string) *binary.Node {
	interactive := binary.Node{
		Tag: "interactive",
		Attrs: binary.Attrs{"type": "native_flow", "v": "1"},
		Content: []binary.Node{
			{
				Tag:   "native_flow",
				Attrs: binary.Attrs{"name": name},
			},
		},
	}
	return &binary.Node{
		Tag:     "biz",
		Content: []binary.Node{interactive},
	}
}

// botXMLNode constructs <bot biz_bot="1"/>.
func botXMLNode() *binary.Node {
	return &binary.Node{
		Tag:   "bot",
		Attrs: binary.Attrs{"biz_bot": "1"},
	}
}

func formatBody(body, header string) string {
	if header == "" {
		return body
	}
	return header + "\n\n" + body
}

// buildNativeFlowButton constructs the per-type NativeFlowButton.
func buildNativeFlowButton(b ButtonRequest) (*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, error) {
	switch b.Type {
	case "url":
		params, _ := json.Marshal(map[string]string{
			"display_text": b.Label,
			"url":          b.URL,
			"merchant_url": b.URL,
		})
		return &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
			Name:             proto.String("cta_url"),
			ButtonParamsJSON: proto.String(string(params)),
		}, nil
	case "copy":
		code := b.Code
		if code == "" {
			code = b.ID
		}
		id := b.ID
		if id == "" {
			id = "copy_" + strconv.FormatInt(time.Now().UnixNano(), 10)
		}
		params, _ := json.Marshal(map[string]string{
			"display_text": b.Label,
			"id":           id,
			"copy_code":    code,
		})
		return &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
			Name:             proto.String("cta_copy"),
			ButtonParamsJSON: proto.String(string(params)),
		}, nil
	case "call":
		params, _ := json.Marshal(map[string]string{
			"display_text": b.Label,
			"phone_number":  b.PhoneNumber,
		})
		return &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
			Name:             proto.String("cta_call"),
			ButtonParamsJSON: proto.String(string(params)),
		}, nil
	case "pix":
		return buildPixNativeFlowButton(b)
	}
	return nil, fmt.Errorf("unknown button type: %s", b.Type)
}

// buildPixNativeFlowButton builds the payment_info NativeFlowButton.
func buildPixNativeFlowButton(b ButtonRequest) (*waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton, error) {
	refID, err := randomID(11)
	if err != nil {
		return nil, err
	}
	payload := map[string]interface{}{
		"currency":     b.Currency,
		"total_amount": map[string]interface{}{"value": 0, "offset": 100},
		"reference_id": refID,
		"type":         "physical-goods",
		"order": map[string]interface{}{
			"status":     "pending",
			"subtotal":   map[string]interface{}{"value": 0, "offset": 100},
			"order_type": "ORDER",
			"items": []map[string]interface{}{{
				"name":        "",
				"amount":      map[string]interface{}{"value": 0, "offset": 100},
				"quantity":    0,
				"sale_amount": map[string]interface{}{"value": 0, "offset": 100},
			}},
		},
		"payment_settings": []map[string]interface{}{{
			"type": "pix_static_code",
			"pix_static_code": map[string]string{
				"merchant_name": b.Name,
				"key":           b.Key,
				"key_type":      mapPixKeyType(b.KeyType),
			},
		}},
		"share_payment_status": false,
	}
	raw, _ := json.Marshal(payload)
	return &waE2E.InteractiveMessage_NativeFlowMessage_NativeFlowButton{
		Name:             proto.String("payment_info"),
		ButtonParamsJSON: proto.String(string(raw)),
	}, nil
}

func mapPixKeyType(t string) string {
	switch t {
	case "phone":
		return "PHONE"
	case "email":
		return "EMAIL"
	case "cpf":
		return "CPF"
	case "cnpj":
		return "CNPJ"
	case "random":
		return "EVP"
	}
	return ""
}

func randomID(n int) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out), nil
}

func newMessageSecret() ([]byte, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Suppress unused import warnings for things only used by other packages
var _ = base64.RawURLEncoding.EncodeToString
